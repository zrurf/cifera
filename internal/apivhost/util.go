package apivhost

import (
	"net/http"

	"github.com/zrurf/cifera/internal/tenant"
)

// tenantID 返回 RequestContext 中的租户 id
func tenantID(rc *RequestContext) string {
	if rc == nil {
		return ""
	}
	return rc.Tenant.ID
}

// tenantMode 返回 RequestContext 中的租户模式
func tenantMode(rc *RequestContext) tenant.Mode {
	if rc == nil {
		return tenant.ModeAuto
	}
	return rc.Tenant.Mode
}

// requireJar 校验会话存在，缺失时写出 401 并返回错误
func (h *Handler) requireJar(w http.ResponseWriter, rc *RequestContext) error {
	if rc == nil || rc.Jar == nil {
		h.fail(w, http.StatusUnauthorized, ErrCodeNoSession, "无有效会话")
		return errNoSession
	}
	return nil
}

// persist 将 cookie 变更异步持久化
func (h *Handler) persist(rc *RequestContext) {
	if h.cookieMgr != nil && rc != nil && rc.JarKey != "" {
		h.cookieMgr.PersistJar(rc.JarKey, rc.Jar)
	}
}
