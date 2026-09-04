package vhost

import (
	"path/filepath"
	"regexp"
	"testing"
)

// sanitizePath 需彻底阻断目录穿越
func TestSanitizePath(t *testing.T) {
	base := filepath.FromSlash("/srv/www")

	// path.Clean 因前缀 "/" 会把 ../ 归一化到根内，/a/../b 这类输入无法逃逸到 base 外
	okPaths := []string{
		"/index.html",
		"",
		"/assets/js/app.js",
		"/../etc/passwd",
		"/a/../../b",
	}
	for _, p := range okPaths {
		if _, valid := sanitizePath(base, p); !valid {
			t.Errorf("经归一化后落在 base 内的路径 %q 不应被拒绝", p)
		}
	}

	// 反斜杠穿越不被 path 包识别为分隔符，会以 "..\..." 相对路径形态被拒绝
	traversal := []string{
		"..\\..\\windows\\system32",
		"..\\secret",
	}
	for _, p := range traversal {
		got, valid := sanitizePath(base, p)
		if valid {
			t.Errorf("穿越路径 %q 不应通过，结果 %q", p, got)
		}
	}
}

// FallbackMatches：正则在状态码上匹配，支持 ! 前缀取反
func TestFallbackMatches(t *testing.T) {
	h := &Host{}
	h.fallbackRegex = regexp.MustCompile("4..|5..")

	if !h.FallbackMatches(404) || !h.FallbackMatches(502) {
		t.Error("4xx/5xx 应命中默认 fallback 条件")
	}
	if h.FallbackMatches(200) {
		t.Error("200 不应命中 fallback 条件")
	}

	h.fallbackNegate = true
	if !h.FallbackMatches(200) {
		t.Error("取反后 200 应命中")
	}
	if h.FallbackMatches(502) {
		t.Error("取反后 502 不应命中")
	}
}
