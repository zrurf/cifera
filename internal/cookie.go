package internal

import (
	"strings"
)

// rewriteCookieString 重写单个 Set-Cookie 头值
func rewriteCookieString(cookie string) string {
	parts := strings.Split(cookie, ";")
	var kept []string

	for i, part := range parts {
		trimmed := strings.TrimSpace(part)
		lower := strings.ToLower(trimmed)

		// 移除 Domain 属性
		if strings.HasPrefix(lower, "domain=") {
			continue
		}

		// 保留其他属性（Path, HttpOnly, Secure, SameSite, Expires, Max-Age 等）
		kept = append(kept, part)

		// 如果是第一个部分（cookie 名值对），保持原样
		_ = i
	}

	return strings.Join(kept, ";")
}
