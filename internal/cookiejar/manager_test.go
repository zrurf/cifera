package cookiejar

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

// cleanup 应移除过期 jar（内存与 DB），保留并持久化存活 jar
func TestManagerCleanup(t *testing.T) {
	m, err := NewManager(Config{
		Enabled:         true,
		JarCapacity:     500,
		PersistPath:     filepath.Join(t.TempDir(), "db"),
		CleanupInterval: time.Minute,
	}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}

	// 过期 jar：仅含已过期 cookie
	expiredSid := newUUID()
	expiredJar := NewJar(500)
	expiredJar.AddCookie(&http.Cookie{Name: "e", Value: "e", Path: "/"}, "example.com")
	expiredJar.cookies["example.com"][cookieKey("e", "/")].MaxAge = 1
	expiredJar.cookies["example.com"][cookieKey("e", "/")].Created = time.Now().Add(-2 * time.Second)
	m.jars[expiredSid] = expiredJar

	// 存活 jar：含未过期的持久化 cookie
	liveSid := newUUID()
	liveJar := NewJar(500)
	liveJar.AddCookie(&http.Cookie{
		Name: "l", Value: "1", Path: "/",
		Expires: time.Now().Add(time.Hour),
	}, "example.com")
	m.jars[liveSid] = liveJar

	m.cleanup()

	if _, ok := m.jars[expiredSid]; ok {
		t.Error("过期 jar 应从内存移除")
	}
	if _, ok := m.jars[liveSid]; !ok {
		t.Error("存活 jar 应保留在内存")
	}
	if jar := m.loadJar(expiredSid); jar != nil {
		t.Error("过期 jar 应从 DB 删除")
	}
	if jar := m.loadJar(liveSid); jar == nil {
		t.Error("存活 jar 应持久化到 DB")
	} else if cookies := jar.AllCookies(); len(cookies) != 1 {
		t.Errorf("存活 jar 的 cookie 数量应为 1，实际 %d", len(cookies))
	}

	m.Close()
}

// 相同 jarKey 返回同一 jar；不同租户命名空间互相隔离
func TestGetOrCreateJarKey(t *testing.T) {
	m, err := NewManager(Config{JarCapacity: 100}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	jar1, isNew1 := m.GetOrCreateJar("tenantA:sid1")
	if !isNew1 {
		t.Error("首次创建应标记为新会话")
	}
	jar2, isNew2 := m.GetOrCreateJar("tenantA:sid1")
	if isNew2 || jar1 != jar2 {
		t.Error("相同 jarKey 应返回同一个 jar")
	}
	jar3, _ := m.GetOrCreateJar("tenantB:sid1")
	if jar1 == jar3 {
		t.Error("不同租户命名空间应互相隔离")
	}
}

// TestNewSessionID 生成的会话 ID 应为合法 UUID v4
func TestNewSessionID(t *testing.T) {
	sid := NewSessionID()
	if !IsValidSessionID(sid) {
		t.Errorf("NewSessionID 应生成合法 UUID v4，实际 %q", sid)
	}
}
