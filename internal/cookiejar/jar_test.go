package cookiejar

import (
	"net/http"
	"testing"
	"time"
)

// 排序：path 长度降序，同 path 按创建时间升序（RFC 6265 §5.4.2）
func TestJarCookiesOrdering(t *testing.T) {
	j := NewJar(100)
	// 同域下不同 path（/ 与 /app）
	j.AddCookie(&http.Cookie{Name: "root", Value: "1", Path: "/"}, "example.com")
	j.AddCookie(&http.Cookie{Name: "app", Value: "2", Path: "/app"}, "example.com")

	cookies := j.Cookies("example.com", "/app/page")
	if len(cookies) != 2 {
		t.Fatalf("应匹配 2 个 cookie，实际 %d", len(cookies))
	}
	// 更长 path 优先
	if cookies[0].Name != "app" {
		t.Errorf("path 更长的 cookie 应在前，实际 %v", cookies[0].Name)
	}
}

// AddCookie 的删除语义：MaxAge<0 表示删除
func TestJarAddCookieDelete(t *testing.T) {
	j := NewJar(10)
	j.AddCookie(&http.Cookie{Name: "a", Value: "1", Path: "/"}, "example.com")
	if len(j.Cookies("example.com", "/")) != 1 {
		t.Fatal("添加后应有 1 个 cookie")
	}

	// 用 MaxAge<0 删除
	j.AddCookie(&http.Cookie{Name: "a", Value: "1", Path: "/", MaxAge: -1}, "example.com")
	if got := j.Cookies("example.com", "/"); len(got) != 0 {
		t.Fatalf("删除后应无 cookie，实际 %d", len(got))
	}
}

// 容量淘汰：超过容量后淘汰最早创建的 cookie
func TestJarEviction(t *testing.T) {
	j := NewJar(2)
	j.AddCookie(&http.Cookie{Name: "a", Value: "1", Path: "/"}, "example.com")
	j.AddCookie(&http.Cookie{Name: "b", Value: "2", Path: "/"}, "example.com")
	j.AddCookie(&http.Cookie{Name: "c", Value: "3", Path: "/"}, "example.com")

	cookies := j.Cookies("example.com", "/")
	if len(cookies) != 2 {
		t.Fatalf("容量 2 时仅应保留 2 个，实际 %d", len(cookies))
	}
	if cookies[0].Name == "a" || cookies[1].Name == "a" {
		t.Error("最早创建的 cookie a 应被淘汰")
	}
}

// 过期 cookie 不应返回
func TestJarSkipsExpired(t *testing.T) {
	j := NewJar(10)
	j.AddCookie(&http.Cookie{Name: "e", Value: "v", Path: "/", Expires: time.Now().Add(-time.Hour)}, "example.com")
	if got := j.Cookies("example.com", "/"); len(got) != 0 {
		t.Fatalf("过期 cookie 不应返回，实际 %d", len(got))
	}
}
