package cookiejar

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nutsdb/nutsdb"
	"go.uber.org/zap"
)

const bucketName = "cookies"

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
	PersistPath     string        // NutsDB 数据目录，为空则禁用持久化
	CleanupInterval time.Duration // 清理间隔，默认 5 分钟
}

// persistTask 表示一个异步持久化任务
type persistTask struct {
	sessionID string
	entries   []CookieEntry
}

// Manager 管理所有 cookie jar
type Manager struct {
	mu              sync.RWMutex
	jars            map[string]*Jar // key：会话 UUID
	db              *nutsdb.DB      // NutsDB 持久化实例（禁用持久化时为 nil）
	logger          *zap.Logger
	maxPerJar       int
	cleanupInterval time.Duration
	done            chan struct{}
	// 异步持久化通道，缓冲足够大以避免阻塞
	persistCh chan persistTask
}

// NewManager 创建 cookie jar 管理器；
// cfg.PersistPath 非空时打开 NutsDB 并启用持久化。
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
		opt := nutsdb.DefaultOptions
		opt.Dir = cfg.PersistPath
		opt.SyncEnable = false // 关闭同步写以提升性能，可容忍少量数据丢失

		db, err := nutsdb.Open(opt)
		if err != nil {
			return nil, fmt.Errorf("failed to open NutsDB at %s: %w", cfg.PersistPath, err)
		}
		m.db = db

		// 确保 bucket 存在（NutsDB v1.0.0+ 不再自动创建 bucket）
		if err := m.db.Update(func(tx *nutsdb.Tx) error {
			if err := tx.NewBucket(nutsdb.DataStructureBTree, bucketName); err != nil {
				// bucket 已存在不是错误
				if !strings.Contains(err.Error(), "already exist") {
					return err
				}
			}
			return nil
		}); err != nil {
			return nil, fmt.Errorf("failed to create NutsDB bucket: %w", err)
		}

		m.loadAllJars()

		go m.persistWorker()

		logger.Info("Cookie Jar 持久化已启用",
			zap.String("path", cfg.PersistPath),
		)
	}

	return m, nil
}

// persistWorker 从 persistCh 读取任务并写入 NutsDB，避免阻塞请求处理
func (m *Manager) persistWorker() {
	for {
		select {
		case <-m.done:
			// 排空通道中剩余任务
			for len(m.persistCh) > 0 {
				task := <-m.persistCh
				m.writeJarToDB(task.sessionID, task.entries)
			}
			return
		case task := <-m.persistCh:
			m.writeJarToDB(task.sessionID, task.entries)
		}
	}
}

// writeJarToDB 将 cookie entries 写入 NutsDB（仅由 persistWorker 调用）
func (m *Manager) writeJarToDB(sessionID string, entries []CookieEntry) {
	if m.db == nil {
		return
	}

	if len(entries) == 0 {
		m.deleteFromDB(sessionID)
		return
	}

	data, err := json.Marshal(entries)
	if err != nil {
		m.logger.Error("序列化 cookie jar 失败",
			zap.String("session", sessionID),
			zap.Error(err),
		)
		return
	}

	if err := m.db.Update(func(tx *nutsdb.Tx) error {
		return tx.Put(bucketName, []byte(sessionID), data, 0)
	}); err != nil {
		m.logger.Error("持久化 cookie jar 失败",
			zap.String("session", sessionID),
			zap.Error(err),
		)
	}
}

// GetOrCreateJar 获取或创建指定 sessionID 的 jar，返回实际使用的 sessionID 与是否新建（isNew）。
// sessionID 为空或非法 UUID 时自动生成新 UUID。
func (m *Manager) GetOrCreateJar(sessionID string) (*Jar, string, bool) {
	if sessionID == "" || !isValidUUID(sessionID) {
		sessionID = newUUID()
	}

	m.mu.RLock()
	if jar, ok := m.jars[sessionID]; ok {
		m.mu.RUnlock()
		return jar, sessionID, false
	}
	m.mu.RUnlock()

	if m.db != nil {
		if jar := m.loadJar(sessionID); jar != nil {
			m.mu.Lock()
			m.jars[sessionID] = jar
			m.mu.Unlock()
			return jar, sessionID, false
		}
	}

	jar := NewJar(m.maxPerJar)
	m.mu.Lock()
	m.jars[sessionID] = jar
	m.mu.Unlock()

	return jar, sessionID, true
}

// PersistJar 异步持久化 jar 到 NutsDB。
// 提取 cookie entries 后发送到持久化通道，不阻塞调用方。
// 如果通道满则丢弃本次持久化（下次会重试）。
func (m *Manager) PersistJar(sessionID string, jar *Jar) {
	if m.db == nil {
		return
	}

	entries := jar.AllCookies()
	// 非阻塞发送：通道满时丢弃（cleanup 会定期持久化，不会丢数据）
	select {
	case m.persistCh <- persistTask{sessionID: sessionID, entries: entries}:
	default:
		m.logger.Debug("持久化通道满，跳过本次持久化",
			zap.String("session", sessionID),
		)
	}
}

// StartCleanup 启动定期清理 goroutine
func (m *Manager) StartCleanup() {
	go m.cleanupLoop()
}

// Close 停止清理 goroutine 并关闭 NutsDB
func (m *Manager) Close() {
	close(m.done)
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
func (m *Manager) cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	var expiredSessions []string

	for sessionID, jar := range m.jars {
		jar.RemoveExpired()

		if jar.IsExpired() {
			expiredSessions = append(expiredSessions, sessionID)
		}
	}

	for _, sessionID := range expiredSessions {
		delete(m.jars, sessionID)
		if m.db != nil {
			m.deleteFromDB(sessionID)
		}
		m.logger.Debug("清理过期 cookie jar",
			zap.String("session", sessionID),
		)
	}

	// 同步持久化存活的 jars（cleanup 在后台运行，可阻塞写库）
	if m.db != nil {
		for sessionID, jar := range m.jars {
			entries := jar.AllCookies()
			m.writeJarToDB(sessionID, entries)
		}
	}
}

// loadAllJars 启动时从 NutsDB 加载所有已持久化的 jar
func (m *Manager) loadAllJars() {
	if m.db == nil {
		return
	}

	var keys [][]byte
	if err := m.db.View(func(tx *nutsdb.Tx) error {
		ks, _, err := tx.GetAll(bucketName)
		if err != nil {
			// Bucket 可能尚未存在（首次运行）
			if err == nutsdb.ErrBucketNotFound || strings.Contains(err.Error(), "bucket not found") {
				return nil
			}
			return err
		}
		keys = ks
		return nil
	}); err != nil {
		m.logger.Error("从 NutsDB 加载 cookie jar 列表失败", zap.Error(err))
		return
	}

	for _, key := range keys {
		sessionID := string(key)
		if jar := m.loadJar(sessionID); jar != nil {
			m.jars[sessionID] = jar
		}
	}

	m.logger.Info("从 NutsDB 加载 cookie jar 完成",
		zap.Int("count", len(m.jars)),
	)
}

// loadJar 从 NutsDB 加载单个 jar
func (m *Manager) loadJar(sessionID string) *Jar {
	if m.db == nil {
		return nil
	}

	var data []byte
	if err := m.db.View(func(tx *nutsdb.Tx) error {
		val, err := tx.Get(bucketName, []byte(sessionID))
		if err != nil {
			return err
		}
		data = val
		return nil
	}); err != nil {
		if err != nutsdb.ErrKeyNotFound && !strings.Contains(err.Error(), "key not found") {
			m.logger.Debug("从 NutsDB 加载 cookie jar 失败",
				zap.String("session", sessionID),
				zap.Error(err),
			)
		}
		return nil
	}

	var entries []CookieEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		m.logger.Error("反序列化 cookie jar 失败",
			zap.String("session", sessionID),
			zap.Error(err),
		)
		return nil
	}

	jar := NewJar(m.maxPerJar)
	jar.RestoreCookies(entries)
	return jar
}

// deleteFromDB 从 NutsDB 删除一个 jar
func (m *Manager) deleteFromDB(sessionID string) {
	if m.db == nil {
		return
	}

	if err := m.db.Update(func(tx *nutsdb.Tx) error {
		return tx.Delete(bucketName, []byte(sessionID))
	}); err != nil {
		// 忽略 key/bucket 不存在的错误
		if err != nutsdb.ErrKeyNotFound && !strings.Contains(err.Error(), "not found") {
			m.logger.Error("从 NutsDB 删除 cookie jar 失败",
				zap.String("session", sessionID),
				zap.Error(err),
			)
		}
	}
}
