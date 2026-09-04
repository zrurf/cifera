package apivhost

import (
	"net/http"
	"path"
	"time"
)

type cookieReadBody struct {
	Domain string `json:"domain"` // 必填：目标域（跨域读）
	Path   string `json:"path"`   // 可选：路径过滤，空则匹配全部路径
	// 可选筛选条件，全部命中才返回（name 之外的字段按“未设置即忽略”处理）
	Name     string `json:"name"`     // 名称，支持 * 与 ? 通配（如 "session_*"）
	Secure   *bool  `json:"secure"`   // 仅返回 secure=true/false 的 cookie
	HTTPOnly *bool  `json:"httpOnly"` // 仅返回 httpOnly=true/false 的 cookie
	Session  *bool  `json:"session"`  // true=仅会话 cookie；false=仅持久化 cookie
}

type cookieDTO struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Path     string    `json:"path"`
	Domain   string    `json:"domain,omitempty"`
	Secure   bool      `json:"secure"`
	HttpOnly bool      `json:"httpOnly"`
	Expires  time.Time `json:"expires,omitempty"`
}

func (h *Handler) handleCookieRead(w http.ResponseWriter, r *http.Request) {
	ctx := rc(r)
	if ctx == nil || ctx.Jar == nil {
		h.fail(w, http.StatusUnauthorized, ErrCodeNoSession, "无有效会话")
		return
	}

	var body cookieReadBody
	if err := decodeJSON(r, &body); err != nil {
		h.fail(w, http.StatusBadRequest, ErrCodeBadRequest, "请求参数错误: "+err.Error())
		return
	}
	if body.Domain == "" {
		h.fail(w, http.StatusBadRequest, ErrCodeBadRequest, "domain 不能为空")
		return
	}
	if body.Path == "" {
		body.Path = "/"
	}

	cookies := ctx.Jar.Cookies(body.Domain, body.Path)
	out := make([]cookieDTO, 0, len(cookies))
	for _, c := range cookies {
		if !matchCookieFilter(c, body) {
			continue
		}
		dto := cookieDTO{Name: c.Name, Value: c.Value, Path: c.Path, Secure: c.Secure, HttpOnly: c.HttpOnly}
		if !c.Expires.IsZero() {
			dto.Expires = c.Expires
		}
		out = append(out, dto)
	}
	h.ok(w, out)
}

// matchCookieFilter 判断 cookie 是否命中全部筛选条件（未设置的字段忽略）
func matchCookieFilter(c *http.Cookie, f cookieReadBody) bool {
	if f.Name != "" {
		// path.Match 提供 * 与 ? 通配；cookie 名不含 '/'，与 path 语义无冲突
		if matched, err := path.Match(f.Name, c.Name); err != nil || !matched {
			return false
		}
	}
	if f.Secure != nil && c.Secure != *f.Secure {
		return false
	}
	if f.HTTPOnly != nil && c.HttpOnly != *f.HTTPOnly {
		return false
	}
	// 会话 cookie：Expires 零值且 MaxAge 为 0
	isSession := c.Expires.IsZero() && c.MaxAge == 0
	if f.Session != nil && isSession != *f.Session {
		return false
	}
	return true
}

type cookieWriteBody struct {
	Domain   string    `json:"domain"`
	Path     string    `json:"path"`
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Expires  time.Time `json:"expires"`
	MaxAge   int       `json:"maxAge"`
	Secure   bool      `json:"secure"`
	HttpOnly bool      `json:"httpOnly"`
}

func (h *Handler) handleCookieSet(w http.ResponseWriter, r *http.Request) {
	ctx := rc(r)
	if err := h.requireJar(w, ctx); err != nil {
		return
	}

	var body cookieWriteBody
	if err := decodeJSON(r, &body); err != nil {
		h.fail(w, http.StatusBadRequest, ErrCodeBadRequest, "请求参数错误: "+err.Error())
		return
	}
	if body.Name == "" || body.Domain == "" {
		h.fail(w, http.StatusBadRequest, ErrCodeBadRequest, "name 与 domain 不能为空")
		return
	}
	if body.Path == "" {
		body.Path = "/"
	}

	cookie := &http.Cookie{
		Name:     body.Name,
		Value:    body.Value,
		Path:     body.Path,
		Expires:  body.Expires,
		MaxAge:   body.MaxAge,
		Secure:   body.Secure,
		HttpOnly: body.HttpOnly,
	}
	ctx.Jar.AddCookie(cookie, body.Domain)
	h.persist(ctx)
	h.ok(w, map[string]any{"ok": true})
}

func (h *Handler) handleCookieDelete(w http.ResponseWriter, r *http.Request) {
	ctx := rc(r)
	if err := h.requireJar(w, ctx); err != nil {
		return
	}

	var body cookieWriteBody
	if err := decodeJSON(r, &body); err != nil {
		h.fail(w, http.StatusBadRequest, ErrCodeBadRequest, "请求参数错误: "+err.Error())
		return
	}
	if body.Name == "" {
		h.fail(w, http.StatusBadRequest, ErrCodeBadRequest, "name 不能为空")
		return
	}

	count := ctx.Jar.RemoveCookie(body.Name, body.Domain, body.Path)
	h.persist(ctx)
	h.ok(w, map[string]any{"deleted": count})
}
