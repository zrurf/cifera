package rewriter

import (
	"strings"
	"testing"
)

const testProxyBase = "http://127.0.0.1:8080"

func TestLooksLikeHTML(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{"<html><head><title>x</title></head></html>", true},
		{"<!doctype html><html>", true},
		{"<div>hello</div>", true},
		{`{"a":1,"b":2}`, false},
		{"plain text without any html tags", false},
	}
	for _, c := range cases {
		if got := looksLikeHTML([]byte(c.body)); got != c.want {
			t.Errorf("looksLikeHTML(%q) = %v, 期望 %v", c.body, got, c.want)
		}
	}
}

func TestRewriteHTMLUrls(t *testing.T) {
	html := `<html><head><link href="/assets/site.css"></head><body>` +
		`<a href="/page/2">2</a>` +
		`<img src="https://cdn.example.com/i.png">` +
		`<div style="background:url(/bg.png)"></div>` +
		`<script src="//cdn.example.com/app.js"></script>` +
		`<meta http-equiv="refresh" content="0;url=/new">` +
		`</body></html>`

	out := string(RewriteHTMLUrls([]byte(html), testProxyBase, "/page/index", "example.com", "https"))

	checks := []string{
		// 相对路径改写为代理 URL
		`href="http://127.0.0.1:8080/page/2?_cifera_h=example.com&_cifera_s=https"`,
		// 绝对 URL 改写为代理 URL（保留原 host/scheme）
		`src="http://127.0.0.1:8080/i.png?_cifera_h=cdn.example.com&_cifera_s=https"`,
		// style 内 CSS url() 改写
		`background:url('http://127.0.0.1:8080/bg.png?_cifera_h=example.com&_cifera_s=https')`,
		// 协议相对 URL 改写
		`src="http://127.0.0.1:8080/app.js?_cifera_h=cdn.example.com&_cifera_s=https"`,
		// meta refresh 改写
		`content="0;url=http://127.0.0.1:8080/new?_cifera_h=example.com&_cifera_s=https"`,
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("改写结果缺少 %q\n实际输出: %s", want, out)
		}
	}
}

func TestInjectRuntime(t *testing.T) {
	html := []byte("<html><head></head><body>hi</body></html>")
	out := string(InjectRuntime(html, "console.log(1)", "example.com", "https", "https://example.com/page", ""))

	if !strings.HasPrefix(out, "<html><head><script>var __CIFERA__=") {
		t.Errorf("JS 运行时应注入到 <head> 之后，实际开头: %q", out[:min(len(out), 60)])
	}
	if !strings.Contains(out, `console.log(1)`) {
		t.Error("JS 运行时内容缺失")
	}
	if !strings.Contains(out, `"h":"example.com"`) || !strings.Contains(out, `"s":"https"`) {
		t.Error("__CIFERA__ 配置缺失")
	}
}

func TestInjectRuntimeWithCookies(t *testing.T) {
	html := []byte("<head></head>")
	cookiesJSON := `[{"n":"a","v":"1","p":"/"}]`
	out := string(InjectRuntime(html, "/*js*/", "example.com", "http", "", cookiesJSON))
	// cookiesJSON 应以原始 JSON 直接内联，不做二次编码
	if !strings.Contains(out, `"c":[{"n":"a","v":"1","p":"/"}]`) {
		t.Errorf("cookie 配置应内联原始 JSON，实际: %s", out)
	}
}
