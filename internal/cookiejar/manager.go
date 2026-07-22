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

// uuidV4Regex matches UUID v4 format.
var uuidV4Regex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// isValidUUID checks if a string is a valid UUID v4.
func isValidUUID(s string) bool {
	return uuidV4Regex.MatchString(s)
}

// newUUID generates a new UUID v4 using crypto/rand.
func newUUID() string {
	var uuid [16]byte
	_, _ = rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16])
}

// Config for cookie jar manager.
type Config struct {
	Enabled         bool
	JarCapacity     int           // max cookies per jar, default 500
	PersistPath     string        // NutsDB data directory, empty = no persistence
	CleanupInterval time.Duration // cleanup interval, default 5 minutes
}

// persistTask 表示一个异步持久化任务
type persistTask struct {
	sessionID string
	entries   []CookieEntry
}

// Manager manages all cookie jars.
type Manager struct {
	mu              sync.RWMutex
	jars            map[string]*Jar // key: session UUID
	db              *nutsdb.DB      // NutsDB for persistence (nil if disabled)
	logger          *zap.Logger
	maxPerJar       int
	cleanupInterval time.Duration
	done            chan struct{}
	// 异步持久化通道，缓冲足够大以避免阻塞
	persistCh chan persistTask
}

// NewManager creates a new cookie jar manager.
// If cfg.PersistPath is non-empty, it opens NutsDB for persistence.
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

	// Open NutsDB if persistence is configured
	if cfg.PersistPath != "" {
		opt := nutsdb.DefaultOptions
		opt.Dir = cfg.PersistPath
		opt.SyncEnable = false // Better performance; we can tolerate some data loss

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

		// Load all persisted jars on startup
		m.loadAllJars()

		// 启动异步持久化 worker
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

// GetOrCreateJar gets an existing jar or creates a new one.
// If sessionID is empty, a new UUID is generated.
// If sessionID is invalid UUID format, a new UUID is generated.
// Returns (jar, sessionID, isNew). The sessionID is the actual ID used (may be newly generated).
func (m *Manager) GetOrCreateJar(sessionID string) (*Jar, string, bool) {
	// Validate session ID
	if sessionID == "" || !isValidUUID(sessionID) {
		sessionID = newUUID()
	}

	m.mu.RLock()
	if jar, ok := m.jars[sessionID]; ok {
		m.mu.RUnlock()
		return jar, sessionID, false
	}
	m.mu.RUnlock()

	// Try to load from persistence
	if m.db != nil {
		if jar := m.loadJar(sessionID); jar != nil {
			m.mu.Lock()
			m.jars[sessionID] = jar
			m.mu.Unlock()
			return jar, sessionID, false
		}
	}

	// Create new jar
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
		// 通道满，丢弃本次持久化
		m.logger.Debug("持久化通道满，跳过本次持久化",
			zap.String("session", sessionID),
		)
	}
}

// StartCleanup starts the periodic cleanup goroutine.
func (m *Manager) StartCleanup() {
	go m.cleanupLoop()
}

// Close stops the cleanup goroutine and closes NutsDB.
func (m *Manager) Close() {
	close(m.done)
	if m.db != nil {
		m.db.Close()
	}
}

// cleanupLoop periodically removes expired cookies and all-expired jars.
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

// cleanup removes expired cookies from all jars and removes all-expired jars.
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

	// Remove all-expired jars
	for _, sessionID := range expiredSessions {
		delete(m.jars, sessionID)
		if m.db != nil {
			m.deleteFromDB(sessionID)
		}
		m.logger.Debug("清理过期 cookie jar",
			zap.String("session", sessionID),
		)
	}

	// Persist surviving jars（同步，因为 cleanup 在后台运行）
	if m.db != nil {
		for sessionID, jar := range m.jars {
			entries := jar.AllCookies()
			m.writeJarToDB(sessionID, entries)
		}
	}
}

// loadAllJars loads all persisted jars from NutsDB on startup.
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

// loadJar loads a single jar from NutsDB.
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

// deleteFromDB deletes a jar from NutsDB.
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
