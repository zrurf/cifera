package cookiejar

import "testing"

// RFC 6265 §5.1.3 域名匹配
func TestDomainMatch(t *testing.T) {
	cases := []struct {
		host, domain string
		want         bool
	}{
		{"example.com", "example.com", true},
		{"example.com", ".example.com", true},
		{"sub.example.com", "example.com", true},
		{"sub.example.com", ".example.com", true},
		{"sub.example.com", "sub.example.com", true},
		{"example.com", "sub.example.com", false},
		{"sub.example.com", "other.com", false},
		{"a.example.com", "example.com", true},
	}
	for _, c := range cases {
		if got := domainMatch(c.host, c.domain); got != c.want {
			t.Errorf("domainMatch(%q, %q) = %v, 期望 %v", c.host, c.domain, got, c.want)
		}
	}
}

// RFC 6265 §5.1.4 路径匹配
func TestPathMatch(t *testing.T) {
	cases := []struct {
		reqPath, cookiePath string
		want                bool
	}{
		{"/", "/", true},
		{"/foo", "/foo", true},
		{"/foo/", "/foo", true},
		{"/foo/bar", "/foo", true},
		{"/foobar", "/foo", false},
		{"/foo/bar", "/foo/bar", true},
		{"/foo/bar/baz", "/foo/bar", true},
		{"/FOO", "/foo", false},
	}
	for _, c := range cases {
		if got := pathMatch(c.reqPath, c.cookiePath); got != c.want {
			t.Errorf("pathMatch(%q, %q) = %v, 期望 %v", c.reqPath, c.cookiePath, got, c.want)
		}
	}
}

func TestCanonicalDomain(t *testing.T) {
	if got := canonicalDomain(".Example.COM"); got != "example.com" {
		t.Errorf("canonicalDomain = %q, 期望 example.com", got)
	}
}
