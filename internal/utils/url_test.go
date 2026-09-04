package utils

import (
	"net/url"
	"testing"
)

func TestParseProxyUrl(t *testing.T) {
	u, _ := url.Parse("http://127.0.0.1:8080/img?a=1&_cifera_h=example.com&_cifera_s=https")
	origin, schema, host := ParseProxyUrl(u)
	if host != "example.com" {
		t.Errorf("host = %q, 期望 example.com", host)
	}
	if schema != "https" {
		t.Errorf("schema = %q, 期望 https", schema)
	}
	// 代理参数应从 origin 中被剥离
	if origin != "https://example.com/img?a=1" {
		t.Errorf("origin = %q", origin)
	}
}

func TestBuildProxyUrl(t *testing.T) {
	const proxyBase = "http://127.0.0.1:8080"
	const currentPath = "/page/index"
	const host = "example.com"
	const schema = "https"

	// 相对路径：基于当前路径解析并追加代理参数
	got := BuildProxyUrl(proxyBase, "/assets/app.js", currentPath, host, schema)
	expected := "http://127.0.0.1:8080/assets/app.js?_cifera_h=example.com&_cifera_s=https"
	if got != expected {
		t.Errorf("相对路径 = %q, 期望 %q", got, expected)
	}

	// 协议相对 URL：目标 host 使用 URL 自身 host，schema 回退为代理 schema
	got = BuildProxyUrl(proxyBase, "//cdn.example.com/app.js", currentPath, host, schema)
	if got != "http://127.0.0.1:8080/app.js?_cifera_h=cdn.example.com&_cifera_s=https" {
		t.Errorf("协议相对 = %q", got)
	}

	// 绝对 URL：完整改写
	got = BuildProxyUrl(proxyBase, "https://img.example.com/a.png", currentPath, host, "http")
	if got != "http://127.0.0.1:8080/a.png?_cifera_h=img.example.com&_cifera_s=https" {
		t.Errorf("绝对 URL = %q", got)
	}

	// 未知协议原样返回
	if got := BuildProxyUrl(proxyBase, "jsbridge://call/some", currentPath, host, schema); got != "jsbridge://call/some" {
		t.Errorf("未知协议应跳过，实际 %q", got)
	}

	// 纯锚点原样返回
	if got := BuildProxyUrl(proxyBase, "#section", currentPath, host, schema); got != "#section" {
		t.Errorf("锚点应跳过，实际 %q", got)
	}

	// 已含代理参数的 URL 不再改写
	if got := BuildProxyUrl(proxyBase, "/x?_cifera_h=other.com", currentPath, host, schema); got != "/x?_cifera_h=other.com" {
		t.Errorf("已含代理参数应跳过，实际 %q", got)
	}
}

func TestStripMetaParamsAndHeaders(t *testing.T) {
	q := url.Values{"a": {"1"}, "_cifera_h": {"example.com"}, "b": {"2"}}
	StripMetaParams(q)
	if _, ok := q["_cifera_h"]; ok {
		t.Error("_cifera_* 参数应被移除")
	}
	if q.Get("a") != "1" || q.Get("b") != "2" {
		t.Error("普通参数应保留")
	}

	h := map[string][]string{"Cifera-Referer": {"x"}, "Referer": {"y"}, "Cifera-Cookie-Sync": {"z"}}
	StripMetaHeaders(h)
	if _, ok := h["Cifera-Referer"]; ok {
		t.Error("Cifera-* 头应被移除")
	}
	if len(h) != 1 {
		t.Errorf("应仅保留 Referer，实际 %v", h)
	}
}
