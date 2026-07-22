package cookiejar

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// CookieEntry represents a stored cookie with metadata.
type CookieEntry struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`    // original domain from Set-Cookie (lowercase, no leading dot)
	Path     string    `json:"path"`      // cookie path
	Expires  time.Time `json:"expires"`   // zero value means session cookie
	MaxAge   int       `json:"max_age"`   // Max-Age attribute, 0 means not set
	Secure   bool      `json:"secure"`    // Secure attribute
	HttpOnly bool      `json:"http_only"` // HttpOnly attribute
	SameSite int       `json:"same_site"` // SameSite attribute (0=default, 1=Lax, 2=Strict, 3=None)
	Created  time.Time `json:"created"`   // when the cookie was created
	HostOnly bool      `json:"host_only"` // true if Domain attribute was not set
}

// isSessionCookie returns true if this is a session cookie (no expiry).
func (e *CookieEntry) isSessionCookie() bool {
	return e.Expires.IsZero() && e.MaxAge == 0
}

// isExpired returns true if the cookie has expired.
func (e *CookieEntry) isExpired() bool {
	if e.isSessionCookie() {
		return false
	}
	if e.MaxAge > 0 {
		return time.Since(e.Created) > time.Duration(e.MaxAge)*time.Second
	}
	return !e.Expires.IsZero() && time.Now().After(e.Expires)
}

// Jar stores cookies for a single session, keyed by (domain, path, name).
type Jar struct {
	mu      sync.RWMutex
	cookies map[string]map[string]*CookieEntry // key1: domain (lowercase), key2: name+"\x00"+path
	maxSize int
}

// NewJar creates an empty cookie jar with the given max capacity.
func NewJar(maxSize int) *Jar {
	if maxSize <= 0 {
		maxSize = 500
	}
	return &Jar{
		cookies: make(map[string]map[string]*CookieEntry),
		maxSize: maxSize,
	}
}

// cookieKey returns the map key for a cookie entry: name + separator + path.
func cookieKey(name, path string) string {
	return name + "\x00" + path
}

// AddCookie adds or updates a cookie in the jar following RFC 6265 rules.
// The cookie's domain is used as-is (should be canonicalized before calling).
func (j *Jar) AddCookie(cookie *http.Cookie, requestHost string) {
	j.mu.Lock()
	defer j.mu.Unlock()

	entry := &CookieEntry{
		Name:     cookie.Name,
		Value:    cookie.Value,
		Path:     cookie.Path,
		Secure:   cookie.Secure,
		HttpOnly: cookie.HttpOnly,
		SameSite: int(cookie.SameSite),
		Created:  time.Now(),
	}

	// Determine domain and host-only flag
	if cookie.Domain != "" {
		entry.Domain = canonicalDomain(cookie.Domain)
		entry.HostOnly = false
	} else {
		// No Domain attribute: host-only cookie, domain is the request host
		entry.Domain = canonicalDomain(requestHost)
		entry.HostOnly = true
	}

	// Default path to "/" if not set
	if entry.Path == "" {
		entry.Path = "/"
	}

	// Handle expiration
	if cookie.MaxAge > 0 {
		entry.MaxAge = cookie.MaxAge
	} else if cookie.MaxAge < 0 {
		// MaxAge < 0 means delete the cookie
		j.removeCookie(entry.Domain, cookieKey(entry.Name, entry.Path))
		return
	} else if !cookie.Expires.IsZero() {
		entry.Expires = cookie.Expires
	}

	// If the cookie is already expired, don't store it
	if entry.isExpired() {
		return
	}

	domain := entry.Domain
	key := cookieKey(entry.Name, entry.Path)

	if j.cookies[domain] == nil {
		j.cookies[domain] = make(map[string]*CookieEntry)
	}

	// Enforce max size: if we'd exceed capacity, remove the oldest cookie
	if _, exists := j.cookies[domain][key]; !exists {
		j.evictIfNeeded()
	}

	j.cookies[domain][key] = entry
}

// removeCookie removes a cookie from the jar.
func (j *Jar) removeCookie(domain, key string) {
	if m, ok := j.cookies[domain]; ok {
		delete(m, key)
		if len(m) == 0 {
			delete(j.cookies, domain)
		}
	}
}

// evictIfNeeded removes the oldest cookie if the jar is at capacity.
func (j *Jar) evictIfNeeded() {
	total := j.totalCount()
	if total < j.maxSize {
		return
	}

	// Find and remove the oldest cookie
	var oldest *CookieEntry
	var oldestDomain string
	var oldestKey string

	for domain, m := range j.cookies {
		for key, entry := range m {
			if oldest == nil || entry.Created.Before(oldest.Created) {
				oldest = entry
				oldestDomain = domain
				oldestKey = key
			}
		}
	}

	if oldest != nil {
		j.removeCookie(oldestDomain, oldestKey)
	}
}

// totalCount returns the total number of cookies in the jar.
func (j *Jar) totalCount() int {
	count := 0
	for _, m := range j.cookies {
		count += len(m)
	}
	return count
}

// Cookies returns all applicable cookies for the given host and path.
// Cookies are sorted by path length (longest first) per RFC 6265.
func (j *Jar) Cookies(host, path string) []*http.Cookie {
	j.mu.RLock()
	defer j.mu.RUnlock()

	host = strings.ToLower(host)
	var result []*http.Cookie

	for domain, m := range j.cookies {
		// Check domain matching
		if !domainMatch(host, domain) {
			continue
		}

		for _, entry := range m {
			// Check host-only restriction
			if entry.HostOnly && host != domain {
				continue
			}

			// Skip expired cookies
			if entry.isExpired() {
				continue
			}

			// Check path matching
			if !pathMatch(path, entry.Path) {
				continue
			}

			// Secure cookies only sent to HTTPS hosts
			// (the proxy handles HTTP/HTTPS; we skip this check as the proxy
			// itself may be HTTP while the origin is HTTPS)

			result = append(result, &http.Cookie{
				Name:     entry.Name,
				Value:    entry.Value,
				Path:     entry.Path,
				Domain:   entry.Domain,
				Expires:  entry.Expires,
				Secure:   entry.Secure,
				HttpOnly: entry.HttpOnly,
				SameSite: http.SameSite(entry.SameSite),
			})
		}
	}

	// Sort by path length (longest first), then by creation time
	sort.SliceStable(result, func(i, j int) bool {
		if len(result[i].Path) != len(result[j].Path) {
			return len(result[i].Path) > len(result[j].Path)
		}
		return false
	})

	return result
}

// RemoveExpired removes all expired cookies from the jar.
func (j *Jar) RemoveExpired() {
	j.mu.Lock()
	defer j.mu.Unlock()

	for domain, m := range j.cookies {
		for key, entry := range m {
			if entry.isExpired() {
				delete(m, key)
			}
		}
		if len(m) == 0 {
			delete(j.cookies, domain)
		}
	}
}

// IsExpired returns true if all cookies in the jar are expired (or the jar is empty).
func (j *Jar) IsExpired() bool {
	j.mu.RLock()
	defer j.mu.RUnlock()

	for _, m := range j.cookies {
		for _, entry := range m {
			if !entry.isExpired() {
				return false
			}
		}
	}
	return true
}

// AllCookies returns all non-expired cookies in the jar (for persistence).
// Session cookies (no expiry) are NOT included for persistence.
func (j *Jar) AllCookies() []CookieEntry {
	j.mu.RLock()
	defer j.mu.RUnlock()

	var result []CookieEntry
	for _, m := range j.cookies {
		for _, entry := range m {
			if entry.isExpired() {
				continue
			}
			// Skip session cookies for persistence
			if entry.isSessionCookie() {
				continue
			}
			result = append(result, *entry)
		}
	}
	return result
}

// RestoreCookies restores cookies from persistence into the jar.
func (j *Jar) RestoreCookies(entries []CookieEntry) {
	j.mu.Lock()
	defer j.mu.Unlock()

	for i := range entries {
		entry := &entries[i]

		// Skip already expired cookies
		if entry.isExpired() {
			continue
		}

		domain := entry.Domain
		key := cookieKey(entry.Name, entry.Path)

		if j.cookies[domain] == nil {
			j.cookies[domain] = make(map[string]*CookieEntry)
		}
		j.cookies[domain][key] = entry
	}
}
