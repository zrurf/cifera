package apivhost

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zrurf/cifera/internal/cookiejar"
	"github.com/zrurf/cifera/internal/tenant"
	"go.uber.org/zap"
)

func newTestHandler() (*Handler, *cookiejar.Manager) {
	mgr, _ := cookiejar.NewManager(cookiejar.Config{JarCapacity: 100}, zap.NewNop())
	return New(mgr, nil, zap.NewNop()), mgr
}

func doJSON(h *Handler, method, path string, rc *RequestContext, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req, rc)
	return rec
}

// 跨域写 cookie 后，其它领域可读（跨域读写），并支持名称通配与属性筛选
func TestCookieCrossDomainSetAndRead(t *testing.T) {
	h, mgr := newTestHandler()
	defer mgr.Close()

	jar, _ := mgr.GetOrCreateJar("t:s1")
	rc := &RequestContext{Jar: jar, JarKey: "t:s1", Tenant: tenant.FromSession("s1")}

	// 写入多个 cookie（不同名称/属性）
	for _, kv := range []struct {
		name, val string
		httpOnly  bool
		session   bool
	}{
		{"k", "v", false, true},
		{"session_a", "s", false, true},
		{"session_b", "S", true, true},
		{"persist", "p", false, false},
	} {
		body := map[string]any{"domain": "a.example.com", "path": "/", "name": kv.name, "value": kv.val}
		if kv.httpOnly {
			body["httpOnly"] = true
		}
		if !kv.session {
			body["maxAge"] = 3600
		}
		if rec := doJSON(h, "POST", "/api/v1/cookies/set", rc, body); rec.Code != http.StatusOK {
			t.Fatalf("set %s 失败: %d", kv.name, rec.Code)
		}
	}

	read := func(body map[string]any) []cookieDTO {
		rec := doJSON(h, "POST", "/api/v1/cookies/read", rc, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("read 失败: %d %s", rec.Code, rec.Body.String())
		}
		var resp Response
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Code != ErrCodeOK {
			t.Fatalf("响应 code 应为 0，实际 %d %s", resp.Code, rec.Body.String())
		}
		dataBytes, _ := json.Marshal(resp.Data)
		var got []cookieDTO
		if err := json.Unmarshal(dataBytes, &got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	// 基础跨域读
	got := read(map[string]any{"domain": "a.example.com", "path": "/"})
	if len(got) != 4 {
		t.Fatalf("跨域读取数量异常，期望 4 实际 %d: %+v", len(got), got)
	}

	// 名称通配筛选
	got = read(map[string]any{"domain": "a.example.com", "name": "session_*"})
	if len(got) != 2 {
		t.Fatalf("通配筛选应返回 2 个，实际 %d: %+v", len(got), got)
	}

	// httpOnly 筛选
	got = read(map[string]any{"domain": "a.example.com", "httpOnly": true})
	if len(got) != 1 || got[0].Name != "session_b" {
		t.Fatalf("httpOnly 筛选异常: %+v", got)
	}

	// 仅持久化 cookie
	got = read(map[string]any{"domain": "a.example.com", "session": false})
	if len(got) != 1 || got[0].Name != "persist" {
		t.Fatalf("持久化筛选异常: %+v", got)
	}
}

// 无会话时 cookie 写操作应返回 401，且 body 为统一 {code, msg, data} 结构
func TestCookieWithoutSession(t *testing.T) {
	h, mgr := newTestHandler()
	defer mgr.Close()

	rec := doJSON(h, "POST", "/api/v1/cookies/set", &RequestContext{}, map[string]any{
		"domain": "x.com", "name": "k", "value": "v",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("无会话应 401，实际 %d", rec.Code)
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != ErrCodeNoSession {
		t.Errorf("失败响应 code 应为 %d，实际 %d", ErrCodeNoSession, resp.Code)
	}
	if resp.Message == "" {
		t.Error("失败响应 message 不应为空")
	}
}

// 方法不匹配时应返回 405（chi 路由方法感知），而非命中 404
func TestRoutableMethodMismatch(t *testing.T) {
	h, mgr := newTestHandler()
	defer mgr.Close()

	// cookies/read 仅支持 POST，GET 应 405
	rec := doJSON(h, "GET", "/api/v1/cookies/read", &RequestContext{}, nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("方法不匹配应 405，实际 %d", rec.Code)
	}
}

// 未注册路径应 404
func TestUnknownPath404(t *testing.T) {
	h, mgr := newTestHandler()
	defer mgr.Close()
	rec := doJSON(h, "GET", "/api/v1/unknown", &RequestContext{}, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("未知路径应 404，实际 %d", rec.Code)
	}
}

// 注册租户鉴权：校验 secret 后签发 token 并经 Set-Cookie 下发
func TestTenantAuthAndCookie(t *testing.T) {
	store := tenant.NewStore([]tenant.Registration{
		{ID: "acme", Name: "Acme", Enabled: true, TokenTTL: 3600, Secret: "s"},
	})
	mgr, _ := cookiejar.NewManager(cookiejar.Config{JarCapacity: 100}, zap.NewNop())
	defer mgr.Close()
	h := New(mgr, store, zap.NewNop())

	rec := doJSON(h, "POST", "/api/v1/tenant/auth", &RequestContext{}, map[string]any{
		"tenant_id": "acme", "secret": "wrong",
	})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误 secret 应 401，实际 %d", rec.Code)
	}

	rec = doJSON(h, "POST", "/api/v1/tenant/auth", &RequestContext{}, map[string]any{
		"tenant_id": "acme", "secret": "s",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("鉴权失败: %d %s", rec.Code, rec.Body.String())
	}
	setCookies := rec.Result().Cookies()
	if len(setCookies) != 2 {
		t.Fatalf("应下放 tid/tok 两个 cookie，实际 %d", len(setCookies))
	}
}
