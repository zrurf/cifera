package cache

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

// waitForGet 轮询等待异步写入生效
func waitForGet(t *testing.T, c *Cache, key string) (*http.Response, bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if resp, ok := c.Get(key, nil); ok {
			return resp, true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, false
}

func TestCacheSetGet(t *testing.T) {
	c := New(Config{MaxSize: 1 << 20}, zap.NewNop())
	defer c.Close()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
	}
	c.SetAsync("k", resp.Header, []byte("hello"), resp.StatusCode)

	got, ok := waitForGet(t, c, "k")
	if !ok {
		t.Fatal("缓存写入未生效")
	}
	if got.StatusCode != http.StatusOK {
		t.Errorf("状态码 = %d", got.StatusCode)
	}
	body := make([]byte, 5)
	n, _ := got.Body.Read(body)
	if n != 5 || string(body) != "hello" {
		t.Errorf("缓存 body 读取异常: %q", string(body))
	}
}

func TestCacheEvictsOldest(t *testing.T) {
	// 容量极小，最多容纳一个条目
	c := New(Config{MaxSize: 100}, zap.NewNop())
	defer c.Close()

	for i := 0; i < 3; i++ {
		key := strings.Repeat(string(rune('a'+i)), 1) + "-" + string(rune('0'+i))
		h := http.Header{"Content-Type": []string{"text/plain"}}
		c.SetAsync(key, h, []byte(strings.Repeat(key, 40)), http.StatusOK)
	}

	// 轮询直到旧条目被淘汰（异步写入，等待最终状态）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, _, count := c.Stats()
		if count > 0 && count < 3 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("容量 100 字节不应容纳全部 3 个条目")
}

func TestCacheNotModified(t *testing.T) {
	c := New(Config{MaxSize: 1 << 20}, zap.NewNop())
	defer c.Close()

	h := http.Header{"Content-Type": {"text/plain"}}
	h.Set("ETag", `"v1"`) // Set 会规范化为 "Etag"，与 Header.Get 的查找一致
	c.SetAsync("k", h, []byte("hello"), http.StatusOK)

	if _, ok := waitForGet(t, c, "k"); !ok {
		t.Fatal("缓存写入未生效")
	}

	// 普通请求返回完整 200 响应
	resp, _ := c.Get("k", &http.Request{Header: http.Header{"If-None-Match": []string{`"none"`}}})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("条件不匹配时应为 200，实际 %d", resp.StatusCode)
	}

	// If-None-Match 命中 → 304 且无 body
	resp, _ = c.Get("k", &http.Request{Header: http.Header{"If-None-Match": []string{`"v1"`}}})
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("ETag 命中应为 304，实际 %d", resp.StatusCode)
	}
	if resp.Header.Get("ETag") != `"v1"` {
		t.Error("304 响应应回带 ETag")
	}
}

func TestIsCacheable(t *testing.T) {
	newReq := func(method string) *http.Request {
		return &http.Request{Method: method, Header: http.Header{}}
	}
	newResp := func(status int, ct string, respH http.Header) *http.Response {
		r := &http.Response{StatusCode: status}
		r.Header = http.Header{}
		for k, v := range respH {
			r.Header[k] = v
		}
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		return r
	}

	cases := []struct {
		name string
		req  *http.Request
		resp *http.Response
		want bool
	}{
		{"GET 200 text 可缓存", newReq(http.MethodGet), newResp(200, "text/plain", nil), true},
		{"HEAD 不可写入缓存", newReq(http.MethodHead), newResp(200, "text/plain", nil), false},
		{"POST 不可缓存", newReq(http.MethodPost), newResp(200, "text/plain", nil), false},
		{"非 200 不可缓存", newReq(http.MethodGet), newResp(404, "text/plain", nil), false},
		{"HTML 不可缓存", newReq(http.MethodGet), newResp(200, "text/html; charset=utf-8", nil), false},
		{"JSON 不可缓存", newReq(http.MethodGet), newResp(200, "application/json", nil), false},
		{"no-store 不可缓存", newReq(http.MethodGet), newResp(200, "text/plain", http.Header{"Cache-Control": {"no-store"}}), false},
		{"private 不可缓存", newReq(http.MethodGet), newResp(200, "text/plain", http.Header{"Cache-Control": {"private"}}), false},
		{"Vary:* 不可缓存", newReq(http.MethodGet), newResp(200, "text/plain", http.Header{"Vary": {"*"}}), false},
		{"带 Authorization 不可缓存", newReq(http.MethodGet), newResp(200, "text/plain", nil), false},
	}
	for _, c := range cases {
		if c.name == "带 Authorization 不可缓存" {
			c.req.Header.Set("Authorization", "Bearer x")
		}
		if got := IsCacheable(c.resp, c.req); got != c.want {
			t.Errorf("%s: IsCacheable = %v, 期望 %v", c.name, got, c.want)
		}
	}
}
