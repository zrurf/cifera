package cookiejar

import (
	"net"
	"strings"
)

// domainMatch 按 RFC 6265 §5.1.3 判断 host 是否匹配 cookie domain。
// domain "example.com" 与 ".example.com" 均可匹配 "example.com" 及其子域。
func domainMatch(host, cookieDomain string) bool {
	if host == cookieDomain {
		return true
	}

	// cookieDomain 为空仅匹配空 host（防御性分支，host-only 场景由调用方保证）
	if cookieDomain == "" {
		return host == ""
	}

	d := strings.TrimPrefix(cookieDomain, ".")

	if host == d {
		return true
	}
	// 子域匹配：host 以 ".domain" 结尾
	if strings.HasSuffix(host, "."+d) {
		return true
	}

	return false
}

// pathMatch 按 RFC 6265 §5.1.4 判断请求路径是否匹配 cookie path。
// "/foo" 匹配 "/foo"、"/foo/"、"/foo/bar"；"/" 匹配一切。
func pathMatch(requestPath, cookiePath string) bool {
	if requestPath == cookiePath {
		return true
	}

	if strings.HasPrefix(requestPath, cookiePath) {
		if cookiePath == "/" {
			return true
		}
		// 前缀匹配时要求下一位是 "/"：如 "/foo" 匹配 "/foo/bar"，但不匹配 "/foobar"
		if len(requestPath) > len(cookiePath) && requestPath[len(cookiePath)] == '/' {
			return true
		}
		return false
	}

	return false
}

// canonicalDomain 返回规范化 domain（小写、去除前导点）
func canonicalDomain(domain string) string {
	d := strings.TrimSpace(domain)
	d = strings.TrimPrefix(d, ".")
	d = strings.ToLower(d)
	return d
}

// hostIsIP 判断 host 是否为 IP 地址
func hostIsIP(host string) bool {
	h := host
	if strings.Contains(h, ":") {
		h2, _, err := net.SplitHostPort(h)
		if err == nil {
			h = h2
		}
	}
	return net.ParseIP(h) != nil
}
