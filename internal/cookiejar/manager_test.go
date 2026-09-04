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

// 无效或缺失的 sessionID 应自动生成新 UUID
func TestGetOrCreateJarGeneratesSessionID(t *testing.T) {
	m, err := NewManager(Config{JarCapacity: 100}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	for _, sid := range []string{"", "not-a-uuid"} {
		_, actual, isNew := m.GetOrCreateJar(sid)
		if !isNew {
			t.Errorf("sessionID=%q 应视为新会话", sid)
		}
		if !isValidUUID(actual) {
			t.Errorf("应生成合法 UUID，实际 %q", actual)
		}
	}
}
