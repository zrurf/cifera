package internal

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// extractSetCookies extracts all Set-Cookie headers from the response,
// returns them as []*http.Cookie, and removes Set-Cookie headers from the response.
func extractSetCookies(resp *http.Response) []*http.Cookie {
	rawCookies := resp.Header.Values("Set-Cookie")
	if len(rawCookies) == 0 {
		return nil
	}

	var result []*http.Cookie
	for _, raw := range rawCookies {
		cookie, err := http.ParseSetCookie(raw)
		if err != nil {
			continue
		}
		result = append(result, cookie)
	}

	// Remove all Set-Cookie headers from the response
	resp.Header.Del("Set-Cookie")

	return result
}

// buildCookieHeader builds a Cookie header value from a list of cookies.
// The format is "name1=value1; name2=value2; ..."
func buildCookieHeader(cookies []*http.Cookie) string {
	if len(cookies) == 0 {
		return ""
	}

	var parts []string
	for _, c := range cookies {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// cookieSyncEntry is the JSON format for Cifera-Cookie-Sync header
type cookieSyncEntry struct {
	N string `json:"n"` // name
	V string `json:"v"` // value
	P string `json:"p"` // path
	E int64  `json:"e"` // expires (unix timestamp, 0 = session)
	S bool   `json:"s"` // secure
	H bool   `json:"h"` // httponly
}

// decodeCookieSyncHeader decodes the Cifera-Cookie-Sync header value
// Format: base64(JSON array of cookieSyncEntry)
func decodeCookieSyncHeader(headerValue string) ([]*http.Cookie, error) {
	data, err := base64.StdEncoding.DecodeString(headerValue)
	if err != nil {
		return nil, err
	}
	var entries []cookieSyncEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	var cookies []*http.Cookie
	for _, e := range entries {
		c := &http.Cookie{
			Name:     e.N,
			Value:    e.V,
			Path:     e.P,
			Secure:   e.S,
			HttpOnly: e.H,
		}
		if c.Path == "" {
			c.Path = "/"
		}
		if e.E == -1 {
			// 墓碑标记：删除该 cookie
			c.MaxAge = -1
		} else if e.E > 0 {
			c.Expires = time.Unix(e.E, 0)
		}
		cookies = append(cookies, c)
	}
	return cookies, nil
}

// buildCookiesJSON builds a JSON string from cookies in the cookieSyncEntry format
func buildCookiesJSON(cookies []*http.Cookie) string {
	entries := make([]cookieSyncEntry, len(cookies))
	for i, c := range cookies {
		entries[i] = cookieSyncEntry{
			N: c.Name,
			V: c.Value,
			P: c.Path,
			S: c.Secure,
			H: c.HttpOnly,
		}
		if c.Path == "" {
			entries[i].P = "/"
		}
		if !c.Expires.IsZero() {
			entries[i].E = c.Expires.Unix()
		}
	}
	data, _ := json.Marshal(entries)
	return string(data)
}
