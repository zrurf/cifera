package apivhost

import (
	"net/http"

	"github.com/zrurf/cifera/internal/tenant"
)

// handleTenantInfo 返回当前租户信息（注册/自动）
func (h *Handler) handleTenantInfo(w http.ResponseWriter, r *http.Request) {
	ctx := rc(r)
	out := map[string]any{
		"id":   tenantID(ctx),
		"mode": string(tenantMode(ctx)),
	}
	if h.tenantStore != nil {
		if reg, ok := h.tenantStore.Registration(tenantID(ctx)); ok {
			out["name"] = reg.Name
			out["enabled"] = reg.Enabled
		}
	}
	h.ok(w, out)
}

type tenantAuthBody struct {
	TenantID string `json:"tenant_id"`
	Secret   string `json:"secret"`
}

// handleTenantAuth 注册租户鉴权：校验后签发 token，经 Set-Cookie 下放 _cifera_tid/_cifera_tok
func (h *Handler) handleTenantAuth(w http.ResponseWriter, r *http.Request) {
	if h.tenantStore == nil {
		h.fail(w, http.StatusNotFound, ErrCodeNotFound, "未启用注册租户")
		return
	}

	var body tenantAuthBody
	if err := decodeJSON(r, &body); err != nil {
		h.fail(w, http.StatusBadRequest, ErrCodeBadRequest, "请求参数错误: "+err.Error())
		return
	}
	if body.TenantID == "" {
		h.fail(w, http.StatusBadRequest, ErrCodeBadRequest, "tenant_id 不能为空")
		return
	}

	token, err := h.tenantStore.Auth(body.TenantID, body.Secret)
	if err != nil {
		h.fail(w, http.StatusUnauthorized, ErrCodeAuthFailed, err.Error())
		return
	}

	// 用 cookie 承载租户 id 与 token，避免放入 URL 破坏浏览器缓存
	http.SetCookie(w, &http.Cookie{
		Name: tenant.CookieTID, Value: body.TenantID, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: tenant.CookieTok, Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	h.ok(w, map[string]any{"ok": true, "tenant_id": body.TenantID})
}

// handleTokenStatus 查询当前 token 有效性与归属（调试/续期判断）
func (h *Handler) handleTokenStatus(w http.ResponseWriter, r *http.Request) {
	ctx := rc(r)
	active := tenantMode(ctx) == tenant.ModeManaged
	h.ok(w, map[string]any{"mode": string(tenantMode(ctx)), "active": active, "id": tenantID(ctx)})
}
