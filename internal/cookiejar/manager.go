package cookiejar

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/dgraph-io/badger/v4"
	"go.uber.org/zap"
)

// uuidV4Regex 匹配 UUID v4 格式
var uuidV4Regex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// isValidUUID 判断字符串是否为合法 UUID v4
func isValidUUID(s string) bool {
	return uuidV4Regex.MatchString(s)
}

// newUUID 使用 crypto/rand 生成 UUID v4
func newUUID() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// Config cookie jar 管理器配置
type Config struct {
	Enabled         bool
	JarCapacity     int           // 每个 jar 最大 cookie 数，默认 500
	PersistPath     string        // Badger 数据目录，为空则禁用持久化
	CleanupInterval time.Duration // 清理间隔，默认 5 分钟
}

// persistTask 表示一个异步持久化任务
type persistTask struct {
	jarKey  string
	entries []CookieEntry
}

// Manager 管理所有 cookie jar
type Manager struct {
	mu              sync.RWMutex
	jars            map[string]*Jar // key：租户命名空间键（tenantID:sessionID）
	db              *badger.DB      // Badger 持久化实例（禁用持久化时为 nil）
	logger          *zap.Logger
	maxPerJar       int
	cleanupInterval time.Duration
	done            chan struct{}
	// 跟踪后台 goroutine（persistWorker / cleanupLoop），Close 时等待其退出
	wg sync.WaitGroup
	// 异步持久化通道，缓冲足够大以避免阻塞
	persistCh chan persistTask
}

// NewManager 创建 cookie jar 管理器；
// cfg.PersistPath 非空时打开 Badger 并启用持久化。
func NewManager(cfg Config, logger *zap.Logger) (*Manager, error) {
	m := &Manager{
		jars:            make(map[string]*Jar),
		logger:          logger,
		maxPerJar:       cfg.JarCapacity,
		cleanupInterval: cfg.CleanupInterval,
		done:            make(chan struct{}),
		persistCh:       make(chan persistTask, 4096),
	}

	if m.maxPerJar <= 0 {
		m.maxPerJar = 500
	}
	if m.cleanupInterval <= 0 {
		m.cleanupInterval = 5 * time.Minute
	}

	if cfg.PersistPath != "" {
		db, err := badger.Open(badgerOptions(cfg.PersistPath))
		if err != nil {
			return nil, fmt.Errorf("打开 Badger 数据库失败 (%s): %w", cfg.PersistPath, err)
		}
		m.db = db

		m.loadAllJars()

		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.persistWorker()
		}()

		logger.Info("Cookie Jar 持久化已启用",
			zap.String("path", cfg.PersistPath),
		)
	}

	return m, nil
}

// badgerOptions 返回适用于小型 KV 存储的 Badger 配置。
// cookie jar 数据量小，默认 1GB 的 value log 过大且占磁盘，这里调小以降低占用。
func badgerOptions(dir string) badger.Options {
	return badger.DefaultOptions(dir).
		WithLogger(nil). // 关闭默认日志，避免向 stderr 输出大量信息
		WithValueLogFileSize(64 << 20).
		WithMemTableSize(8 << 20)
}

// persistWorker 从 persistCh 读取任务并写入 Badger，避免阻塞请求处理
func (m *Manager) persistWorker() {
	for {
		select {
		case <-m.done:
			// 排空通道中剩余任务
			for len(m.persistCh) > 0 {
				task := <-m.persistCh
				m.writeJarToDB(task.jarKey, task.entries)
			}
			return
		case task := <-m.persistCh:
			m.writeJarToDB(task.jarKey, task.entries)
		}
	}
}

// writeJarToDB 将 cookie entries 写入 Badger（仅由持久化相关 goroutine 调用）
func (m *Manager) writeJarToDB(jarKey string, entries []CookieEntry) {
	if m.db == nil {
		return
	}

	if len(entries) == 0 {
		m.deleteFromDB(jarKey)
		return
	}

	data, err := json.Marshal(entries)
	if err != nil {
		m.logger.Error("序列化 cookie jar 失败",
			zap.String("jarKey", jarKey),
			zap.Error(err),
		)
		return
	}

	if err := m.db.Update(func(txn *badger.Txn) error {
		return txn.Set([]byte(jarKey), data)
	}); err != nil {
		m.logger.Error("持久化 cookie jar 失败",
			zap.String("jarKey", jarKey),
			zap.Error(err),
		)
	}
}

// NewSessionID 生成一个新的会话 UUID v4。
// 会话 ID 是客户端持有的 _cifera_sid cookie 值；
// 实际 jar 在存储中的键为 jarKey = tenantID:sessionID，见 Tenant.JarKey。
func NewSessionID() string {
	return newUUID()
}

// IsValidSessionID 判断客户端传入的会话 ID 是否为合法 UUID v4
func IsValidSessionID(s string) bool {
	return isValidUUID(s)
}

// GetOrCreateJar 获取或创建指定 jarKey 的 jar，返回 jar 与是否新建（isNew）。
// jarKey 由调用方拼装（tenantID:sessionID），管理器不解析其内部结构。
func (m *Manager) GetOrCreateJar(jarKey string) (*Jar, bool) {
	if jarKey == "" {
		return nil, false
	}

	m.mu.RLock()
	if jar, ok := m.jars[jarKey]; ok {
		m.mu.RUnlock()
		return jar, false
	}
	m.mu.RUnlock()

	if m.db != nil {
		if jar := m.loadJar(jarKey); jar != nil {
			m.mu.Lock()
			m.jars[jarKey] = jar
			m.mu.Unlock()
			return jar, false
		}
	}

	jar := NewJar(m.maxPerJar)
	m.mu.Lock()
	m.jars[jarKey] = jar
	m.mu.Unlock()

	return jar, true
}

// PersistJar 异步持久化 jar 到 Badger。
// 提取 cookie entries 后发送到持久化通道，不阻塞调用方。
// 如果通道满则丢弃本次持久化（下次会重试）。
func (m *Manager) PersistJar(jarKey string, jar *Jar) {
	if m.db == nil {
		return
	}

	entries := jar.AllCookies()
	// 非阻塞发送：通道满时丢弃（cleanup 会定期持久化，不会丢数据）
	select {
	case m.persistCh <- persistTask{jarKey: jarKey, entries: entries}:
	default:
		m.logger.Debug("持久化通道满，跳过本次持久化",
			zap.String("jarKey", jarKey),
		)
	}
}

// StartCleanup 启动定期清理 goroutine
func (m *Manager) StartCleanup() {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.cleanupLoop()
	}()
}

// Close 停止后台 goroutine 并关闭 Badger。
// 通知 persistWorker/cleanupLoop 退出并等待其收尾，避免并发写入已关闭的数据库。
func (m *Manager) Close() {
	close(m.done)
	m.wg.Wait()
	if m.db != nil {
		m.db.Close()
	}
}

// cleanupLoop 周期性清理过期 cookie 及全部过期的 jar
func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(m.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

// cleanup 清理所有 jar 中的过期 cookie，并移除全部过期的 jar
// 内存淘汰在锁内完成，DB 写入在锁外进行，避免慢 IO 阻塞会话请求
func (m *Manager) cleanup() {
	survivors := make(map[string]*Jar, len(m.jars))
	var expiredSessions []string

	m.mu.Lock()
	for sessionID, jar := range m.jars {
		jar.RemoveExpired()
		if jar.IsExpired() {
			expiredSessions = append(expiredSessions, sessionID)
			continue
		}
		survivors[sessionID] = jar
	}
	for _, sessionID := range expiredSessions {
		delete(m.jars, sessionID)
		m.logger.Debug("清理过期 cookie jar",
			zap.String("session", sessionID),
		)
	}
	m.mu.Unlock()

	if m.db == nil {
		return
	}

	for _, sessionID := range expiredSessions {
		m.deleteFromDB(sessionID)
	}
	for sessionID, jar := range survivors {
		entries := jar.AllCookies()
		m.writeJarToDB(sessionID, entries)
	}
}

// loadAllJars 启动时从 Badger 加载所有已持久化的 jar（单次遍历读取）
func (m *Manager) loadAllJars() {
	if m.db == nil {
		return
	}

	if err := m.db.View(func(txn *badger.Txn) error {
		it := txn.NewIterator(badger.DefaultIteratorOptions)
		defer it.Close()

		for it.Rewind(); it.Valid(); it.Next() {
			item := it.Item()
			sessionID := string(item.Key())
			data, err := item.ValueCopy(nil)
			if err != nil {
				m.logger.Debug("读取持久化 cookie jar 失败",
					zap.String("session", sessionID),
					zap.Error(err),
				)
				continue
			}
			jar := restoreJar(data, m.maxPerJar, m.logger, sessionID)
			if jar != nil {
				m.jars[sessionID] = jar
			}
		}
		return nil
	}); err != nil {
		m.logger.Error("从 Badger 加载 cookie jar 失败", zap.Error(err))
		return
	}

	m.logger.Info("从 Badger 加载 cookie jar 完成",
		zap.Int("count", len(m.jars)),
	)
}

// loadJar 从 Badger 加载单个 jar
func (m *Manager) loadJar(sessionID string) *Jar {
	if m.db == nil {
		return nil
	}

	var data []byte
	err := m.db.View(func(txn *badger.Txn) error {
		item, err := txn.Get([]byte(sessionID))
		if err != nil {
			return err
		}
		data, err = item.ValueCopy(nil)
		return err
	})
	if err != nil {
		// 键不存在属正常情况，不记录
		if err != badger.ErrKeyNotFound {
			m.logger.Debug("从 Badger 加载 cookie jar 失败",
				zap.String("session", sessionID),
				zap.Error(err),
			)
		}
		return nil
	}

	return restoreJar(data, m.maxPerJar, m.logger, sessionID)
}

// restoreJar 将序列化数据反序列化为 jar
func restoreJar(data []byte, maxPerJar int, logger *zap.Logger, sessionID string) *Jar {
	var entries []CookieEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		logger.Error("反序列化 cookie jar 失败",
			zap.String("session", sessionID),
			zap.Error(err),
		)
		return nil
	}

	jar := NewJar(maxPerJar)
	jar.RestoreCookies(entries)
	return jar
}

// deleteFromDB 从 Badger 删除一个 jar
func (m *Manager) deleteFromDB(sessionID string) {
	if m.db == nil {
		return
	}

	if err := m.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(sessionID))
	}); err != nil {
		m.logger.Error("从 Badger 删除 cookie jar 失败",
			zap.String("session", sessionID),
			zap.Error(err),
		)
	}
}
