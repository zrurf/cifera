package cookiejar

import (
	"net"
	"strings"
)

// domainMatch checks if a host matches a cookie domain per RFC 6265 Section 5.1.3.
// A cookie domain "example.com" matches "example.com" and "sub.example.com".
// A cookie domain ".example.com" (with leading dot) also matches both.
// An exact host-only cookie (empty Domain attribute) matches only the exact host.
func domainMatch(host, cookieDomain string) bool {
	if host == cookieDomain {
		return true
	}

	// Host-only cookie: no Domain attribute was specified.
	// cookieDomain is empty means it's a host-only cookie; handled by caller.
	if cookieDomain == "" {
		return host == ""
	}

	// Strip leading dot from cookie domain for matching
	d := strings.TrimPrefix(cookieDomain, ".")

	// The host must be a domain match: either exact or a subdomain
	if host == d {
		return true
	}
	// Subdomain match: host ends with ".domain"
	if strings.HasSuffix(host, "."+d) {
		return true
	}

	return false
}

// pathMatch checks if a request path matches a cookie path per RFC 6265 Section 5.1.4.
// Cookie path "/foo" matches "/foo", "/foo/", "/foo/bar".
// Cookie path "/" matches everything.
func pathMatch(requestPath, cookiePath string) bool {
	if requestPath == cookiePath {
		return true
	}

	if strings.HasPrefix(requestPath, cookiePath) {
		if cookiePath == "/" {
			return true
		}
		// requestPath starts with cookiePath; it's a match if:
		// - the next character is "/" (e.g., path="/foo" matches "/foo/bar")
		// - requestPath == cookiePath (already handled above)
		if len(requestPath) > len(cookiePath) && requestPath[len(cookiePath)] == '/' {
			return true
		}
		return false
	}

	return false
}

// canonicalDomain returns the canonical form of a domain (lowercase, leading dot stripped).
func canonicalDomain(domain string) string {
	d := strings.TrimSpace(domain)
	d = strings.TrimPrefix(d, ".")
	d = strings.ToLower(d)
	return d
}

// hostIsIP checks if a host is an IP address.
func hostIsIP(host string) bool {
	// Strip port if present
	h := host
	if strings.Contains(h, ":") {
		h2, _, err := net.SplitHostPort(h)
		if err == nil {
			h = h2
		}
	}
	return net.ParseIP(h) != nil
}
