package cookiejar

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// CookieEntry 一条持久化的 cookie 及其元数据
type CookieEntry struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`    // 来自 Set-Cookie 的原始 domain（小写，无前导点）
	Path     string    `json:"path"`      // cookie path
	Expires  time.Time `json:"expires"`   // 零值表示会话 cookie
	MaxAge   int       `json:"max_age"`   // Max-Age 属性，0 表示未设置
	Secure   bool      `json:"secure"`    // Secure 属性
	HttpOnly bool      `json:"http_only"` // HttpOnly 属性
	SameSite int       `json:"same_site"` // SameSite 属性（0=Default, 1=Lax, 2=Strict, 3=None）
	Created  time.Time `json:"created"`   // cookie 创建时间
	HostOnly bool      `json:"host_only"` // 未设置 Domain 属性时为 true
}

// isSessionCookie 判断是否为会话 cookie（无过期时间）
func (e *CookieEntry) isSessionCookie() bool {
	return e.Expires.IsZero() && e.MaxAge == 0
}

// isExpired 判断 cookie 是否已过期
func (e *CookieEntry) isExpired() bool {
	if e.isSessionCookie() {
		return false
	}
	if e.MaxAge > 0 {
		return time.Since(e.Created) > time.Duration(e.MaxAge)*time.Second
	}
	return !e.Expires.IsZero() && time.Now().After(e.Expires)
}

// Jar 存储单个会话的 cookie，按 (domain, path, name) 组织
type Jar struct {
	mu      sync.RWMutex
	cookies map[string]map[string]*CookieEntry // 第一层 key：domain（小写）；第二层 key：name+"\x00"+path
	maxSize int
}

// NewJar 创建空 cookie jar，容量 maxSize（<=0 时默认 500）
func NewJar(maxSize int) *Jar {
	if maxSize <= 0 {
		maxSize = 500
	}
	return &Jar{
		cookies: make(map[string]map[string]*CookieEntry),
		maxSize: maxSize,
	}
}

// cookieKey 返回 cookie 条目在 map 中的键：name + "\x00" + path
func cookieKey(name, path string) string {
	return name + "\x00" + path
}

// AddCookie 按 RFC 6265 规则新增或更新 cookie
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

	if cookie.Domain != "" {
		entry.Domain = canonicalDomain(cookie.Domain)
		entry.HostOnly = false
	} else {
		entry.Domain = canonicalDomain(requestHost)
		entry.HostOnly = true
	}

	if entry.Path == "" {
		entry.Path = "/"
	}

	if cookie.MaxAge > 0 {
		entry.MaxAge = cookie.MaxAge
	} else if cookie.MaxAge < 0 {
		// MaxAge<0 表示删除该 cookie
		j.removeCookie(entry.Domain, cookieKey(entry.Name, entry.Path))
		return
	} else if !cookie.Expires.IsZero() {
		entry.Expires = cookie.Expires
	}

	if entry.isExpired() {
		return
	}

	domain := entry.Domain
	key := cookieKey(entry.Name, entry.Path)

	if j.cookies[domain] == nil {
		j.cookies[domain] = make(map[string]*CookieEntry)
	}

	if _, exists := j.cookies[domain][key]; !exists {
		j.evictIfNeeded()
	}

	j.cookies[domain][key] = entry
}

// removeCookie 删除指定 cookie，domain 下无剩余 cookie 时一并清理
func (j *Jar) removeCookie(domain, key string) {
	if m, ok := j.cookies[domain]; ok {
		delete(m, key)
		if len(m) == 0 {
			delete(j.cookies, domain)
		}
	}
}

// evictIfNeeded 容量已满时淘汰最早创建的 cookie
func (j *Jar) evictIfNeeded() {
	total := j.totalCount()
	if total < j.maxSize {
		return
	}

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

// totalCount 返回 jar 中的 cookie 总数
func (j *Jar) totalCount() int {
	count := 0
	for _, m := range j.cookies {
		count += len(m)
	}
	return count
}

// Cookies 返回匹配 host/path 的适用 cookie，按 path 长度降序（RFC 6265：最长 path 优先）
func (j *Jar) Cookies(host, path string) []*http.Cookie {
	j.mu.RLock()
	defer j.mu.RUnlock()

	host = strings.ToLower(host)
	var matched []*CookieEntry

	for domain, m := range j.cookies {
		if !domainMatch(host, domain) {
			continue
		}

		for _, entry := range m {
			// host-only cookie 仅匹配完全一致的 host
			if entry.HostOnly && host != domain {
				continue
			}

			if entry.isExpired() {
				continue
			}

			if !pathMatch(path, entry.Path) {
				continue
			}

			// 不校验 Secure cookie 的 HTTPS 限制：代理自身可能为 http，而源站为 https
			matched = append(matched, entry)
		}
	}

	// 按 RFC 6265 排序：path 长度降序，同 path 按创建时间升序（早的优先）
	sort.SliceStable(matched, func(i, j int) bool {
		if len(matched[i].Path) != len(matched[j].Path) {
			return len(matched[i].Path) > len(matched[j].Path)
		}
		return matched[i].Created.Before(matched[j].Created)
	})

	result := make([]*http.Cookie, 0, len(matched))
	for _, entry := range matched {
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

	return result
}

// RemoveExpired 删除 jar 中所有已过期的 cookie
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

// IsExpired 判断 jar 是否所有 cookie 均已过期（空 jar 视为过期）
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

// AllCookies 返回所有未过期 cookie（用于持久化），会话 cookie 不参与持久化
func (j *Jar) AllCookies() []CookieEntry {
	j.mu.RLock()
	defer j.mu.RUnlock()

	var result []CookieEntry
	for _, m := range j.cookies {
		for _, entry := range m {
			if entry.isExpired() {
				continue
			}
			if entry.isSessionCookie() {
				continue
			}
			result = append(result, *entry)
		}
	}
	return result
}

// RestoreCookies 将持久化的 cookie 条目恢复到 jar
func (j *Jar) RestoreCookies(entries []CookieEntry) {
	j.mu.Lock()
	defer j.mu.Unlock()

	for i := range entries {
		entry := &entries[i]

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
