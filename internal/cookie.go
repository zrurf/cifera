package internal

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// extractSetCookies 提取响应中所有 Set-Cookie 头，返回 []*http.Cookie，并从响应中移除
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

	resp.Header.Del("Set-Cookie")

	return result
}

// buildCookieHeader 从 cookie 列表构建 Cookie 头，格式为 "name1=value1; name2=value2; ..."
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

// cookieSyncEntry 是 Cifera-Cookie-Sync 头使用的 JSON 条目格式
type cookieSyncEntry struct {
	N string `json:"n"` // name
	V string `json:"v"` // value
	P string `json:"p"` // path
	E int64  `json:"e"` // 过期时间（unix 秒，0 为会话 cookie）
	S bool   `json:"s"` // secure
	H bool   `json:"h"` // httponly
}

// decodeCookieSyncHeader 解码 Cifera-Cookie-Sync 头值（base64 编码的 cookieSyncEntry JSON 数组）
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

// buildCookiesJSON 将 cookie 序列化为 cookieSyncEntry 格式的 JSON 字符串
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

// buildCookiePushHeader 构建 Cifera-Cookie-Push 头值（格式与 Cifera-Cookie-Sync 一致，e=-1 表示删除）
func buildCookiePushHeader(cookies []*http.Cookie) string {
	entries := make([]cookieSyncEntry, 0, len(cookies))
	for _, c := range cookies {
		entry := cookieSyncEntry{
			N: c.Name,
			V: c.Value,
			P: c.Path,
			S: c.Secure,
			H: c.HttpOnly,
		}
		if entry.P == "" {
			entry.P = "/"
		}
		if c.MaxAge < 0 {
			entry.E = -1
		} else if !c.Expires.IsZero() {
			entry.E = c.Expires.Unix()
		}
		entries = append(entries, entry)
	}
	data, _ := json.Marshal(entries)
	return base64.StdEncoding.EncodeToString(data)
}
