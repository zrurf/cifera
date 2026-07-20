package cache

import (
	"container/list"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Config 缓存配置
type Config struct {
	Enabled bool  `mapstructure:"enabled" toml:"enabled"`
	MaxSize int64 `mapstructure:"max_size" toml:"max_size"` // 最大缓存大小（字节），默认 256MB
}

// DefaultConfig 返回默认缓存配置
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		MaxSize: 256 * 1024 * 1024, // 256MB
	}
}

// cacheWriteRequest 异步缓存写入请求
type cacheWriteRequest struct {
	key        string
	header     http.Header
	body       []byte
	statusCode int
}

// entry 缓存条目
type entry struct {
	key        string
	header     http.Header
	body       []byte
	statusCode int
	storedAt   time.Time
	ttl        time.Duration
	size       int64
}

// Cache 内存 LRU 缓存
type Cache struct {
	mu      sync.Mutex
	lru     *list.List
	items   map[string]*list.Element
	curSize int64
	maxSize atomic.Int64
	logger  *zap.Logger
	hits    atomic.Int64
	misses  atomic.Int64

	// 异步写入通道
	writeCh chan *cacheWriteRequest
	done    chan struct{}
}

// New 创建缓存实例，启动后台异步写入 goroutine
func New(cfg Config, logger *zap.Logger) *Cache {
	maxSize := cfg.MaxSize
	if maxSize <= 0 {
		maxSize = 256 * 1024 * 1024
	}

	c := &Cache{
		lru:     list.New(),
		items:   make(map[string]*list.Element),
		maxSize: atomic.Int64{},
		logger:  logger,
		writeCh: make(chan *cacheWriteRequest, 4096), // 缓冲 4096 条写入请求
		done:    make(chan struct{}),
	}
	c.maxSize.Store(maxSize)

	// 启动后台写入 goroutine
	go c.writeLoop()

	return c
}

// writeLoop 后台缓存写入循环
// 所有写入操作由这个 goroutine 串行执行，无需加锁
func (c *Cache) writeLoop() {
	defer close(c.done)

	for req := range c.writeCh {
		c.setLocked(req.key, req.header, req.body, req.statusCode)
	}
}

// Close 关闭缓存，等待后台写入完成
func (c *Cache) Close() {
	close(c.writeCh)
	<-c.done // 等待写入循环结束
}

// Get 从缓存中获取条目
func (c *Cache) Get(key string) (*http.Response, bool) {
	c.mu.Lock()
	elem, ok := c.items[key]
	if !ok {
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}

	e := elem.Value.(*entry)

	// 检查 TTL
	if e.ttl > 0 && time.Since(e.storedAt) > e.ttl {
		c.removeElement(elem)
		c.mu.Unlock()
		c.misses.Add(1)
		return nil, false
	}

	// 移动到 LRU 前端
	c.lru.MoveToFront(elem)
	c.mu.Unlock()

	c.hits.Add(1)

	// 构造 http.Response
	resp := &http.Response{
		StatusCode: e.statusCode,
		Header:     e.header.Clone(),
		Body:       newBytesReadCloser(e.body),
	}
	return resp, true
}

// SetAsync 异步存储条目到缓存
// 不阻塞调用者，写入操作由后台 goroutine 执行
// body 的所有权转移给缓存（调用者不应再修改 body）
func (c *Cache) SetAsync(key string, header http.Header, body []byte, statusCode int) {
	if len(body) == 0 {
		return
	}

	req := &cacheWriteRequest{
		key:        key,
		header:     header.Clone(), // 必须克隆，因为原始 header 可能被回收
		body:       body,           // body 已是拷贝，直接转移所有权
		statusCode: statusCode,
	}

	select {
	case c.writeCh <- req:
		// 成功提交写入请求
	default:
		// 通道已满，丢弃本次写入（避免阻塞请求）
		c.logger.Debug("缓存写入通道已满，丢弃",
			zap.String("key", key),
			zap.Int("ch_len", len(c.writeCh)),
		)
	}
}

// setLocked 实际执行缓存写入（由 writeLoop 调用，无需加锁）
func (c *Cache) setLocked(key string, header http.Header, body []byte, statusCode int) {
	if len(body) == 0 {
		return
	}

	// 计算 TTL
	ttl := extractTTLFromHeader(header)
	if ttl == 0 {
		ttl = 5 * time.Minute
	} else if ttl < 0 {
		return
	}

	entrySize := int64(len(body)) + estimateHeaderSize(header)

	c.mu.Lock()
	defer c.mu.Unlock()

	// 如果已存在，先移除旧条目
	if elem, ok := c.items[key]; ok {
		c.removeElementLocked(elem)
	}

	// 确保有足够空间
	maxSize := c.maxSize.Load()
	for c.curSize+entrySize > maxSize && c.lru.Len() > 0 {
		oldest := c.lru.Back()
		if oldest != nil {
			c.removeElementLocked(oldest)
		}
	}

	// 存储新条目
	e := &entry{
		key:        key,
		header:     header,
		body:       body,
		statusCode: statusCode,
		storedAt:   time.Now(),
		ttl:        ttl,
		size:       entrySize,
	}

	elem := c.lru.PushFront(e)
	c.items[key] = elem
	c.curSize += entrySize
}

// Resize 动态调整缓存最大容量
func (c *Cache) Resize(newMaxSize int64) {
	if newMaxSize <= 0 {
		return
	}
	c.maxSize.Store(newMaxSize)

	c.mu.Lock()
	defer c.mu.Unlock()

	// 淘汰超出新容量的条目
	for c.curSize > newMaxSize && c.lru.Len() > 0 {
		oldest := c.lru.Back()
		if oldest != nil {
			c.removeElementLocked(oldest)
		}
	}
}

// Stats 返回缓存统计信息
func (c *Cache) Stats() (hits, misses int64, size int64, count int) {
	c.mu.Lock()
	count = c.lru.Len()
	size = c.curSize
	c.mu.Unlock()
	hits = c.hits.Load()
	misses = c.misses.Load()
	return
}

// BuildCacheKey 构建缓存 key
// 使用原始 URL（去掉 _cifera_* 参数后）作为 key
// 同一资源即使 referer 不同，也命中同一缓存条目
func BuildCacheKey(schema, host string, reqURL string) string {
	var sb strings.Builder
	sb.WriteString(schema)
	sb.WriteString("://")
	sb.WriteString(host)
	sb.WriteString(reqURL)
	return sb.String()
}

// IsCacheable 判断响应是否可缓存
func IsCacheable(resp *http.Response, r *http.Request) bool {
	// 仅缓存 GET/HEAD
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	// 仅缓存 200 响应
	if resp.StatusCode != http.StatusOK {
		return false
	}

	// 检查 Content-Type：不缓存 HTML（HTML 需要改写，缓存策略不同），不缓存JSON
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/json") {
		return false
	}

	// 检查 Cache-Control
	cc := resp.Header.Get("Cache-Control")
	if cc != "" {
		ccLower := strings.ToLower(cc)
		if strings.Contains(ccLower, "no-store") || strings.Contains(ccLower, "private") {
			return false
		}
	}

	// 检查 Authorization 头（安全考虑，不缓存需要认证的响应）
	if r.Header.Get("Authorization") != "" {
		return false
	}

	// 不缓存过大的响应（>10MB）
	if resp.ContentLength > 10*1024*1024 {
		return false
	}

	// 不缓存 Vary: * 的响应
	if vary := resp.Header.Get("Vary"); vary == "*" {
		return false
	}

	return true
}

// --- 内部方法 ---

func (c *Cache) removeElement(elem *list.Element) {
	e := elem.Value.(*entry)
	delete(c.items, e.key)
	c.lru.Remove(elem)
	c.curSize -= e.size
}

func (c *Cache) removeElementLocked(elem *list.Element) {
	c.removeElement(elem)
}

// extractTTLFromHeader 从响应头提取缓存 TTL
// 返回 0 表示使用默认 TTL，负数表示不缓存
func extractTTLFromHeader(header http.Header) time.Duration {
	cc := header.Get("Cache-Control")
	if cc != "" {
		ccLower := strings.ToLower(cc)
		if strings.Contains(ccLower, "no-store") || strings.Contains(ccLower, "no-cache") {
			return -1
		}
		if strings.Contains(ccLower, "max-age=") {
			if maxAge := parseCacheControlDirective(cc, "max-age"); maxAge > 0 {
				return time.Duration(maxAge) * time.Second
			}
		}
		if strings.Contains(ccLower, "s-maxage=") {
			if sMaxAge := parseCacheControlDirective(cc, "s-maxage"); sMaxAge > 0 {
				return time.Duration(sMaxAge) * time.Second
			}
		}
	}

	if expires := header.Get("Expires"); expires != "" {
		if t, err := http.ParseTime(expires); err == nil {
			if ttl := time.Until(t); ttl > 0 {
				return ttl
			}
		}
	}

	return 0
}

func parseCacheControlDirective(cc, directive string) int {
	for _, part := range strings.Split(cc, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(strings.ToLower(part), directive+"=") {
			val := part[len(directive)+1:]
			if n, err := strconv.Atoi(val); err == nil {
				return n
			}
		}
	}
	return 0
}

func estimateHeaderSize(h http.Header) int64 {
	size := int64(0)
	for k, vs := range h {
		size += int64(len(k))
		for _, v := range vs {
			size += int64(len(v))
		}
	}
	return size
}

// bytesReadCloser 包装 []byte 为 io.ReadCloser
type bytesReadCloser struct {
	data []byte
	pos  int
}

func newBytesReadCloser(data []byte) *bytesReadCloser {
	return &bytesReadCloser{data: data}
}

func (r *bytesReadCloser) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func (r *bytesReadCloser) Close() error {
	r.pos = len(r.data)
	return nil
}
