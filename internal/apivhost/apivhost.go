// Package apivhost 实现内置 API 虚拟主机（保留主机名，永不转发）。
// 提供跨域 cookie 读写、注册租户鉴权与租户信息查询等能力。
// 路由采用 chi（方法感知、自动 404/405）；响应统一为 {code, msg, data} 结构。
package apivhost

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/zrurf/cifera/internal/cookiejar"
	"github.com/zrurf/cifera/internal/tenant"
	"go.uber.org/zap"
)

// RequestContext 由 server 层注入的请求上下文（jar 已在 cookie 引导阶段就绪）
type RequestContext struct {
	Jar       *cookiejar.Jar
	JarKey    string
	SessionID string
	Tenant    tenant.Tenant
}

// rcKey 在请求 context 中标识 RequestContext
type rcKey struct{}

// withRC 将 RequestContext 注入请求 context
func withRC(ctx context.Context, rc *RequestContext) context.Context {
	return context.WithValue(ctx, rcKey{}, rc)
}

// rc 从请求 context 读取 RequestContext
func rc(r *http.Request) *RequestContext {
	rc, _ := r.Context().Value(rcKey{}).(*RequestContext)
	return rc
}

// Handler 内置 API 处理器
type Handler struct {
	cookieMgr   *cookiejar.Manager
	tenantStore *tenant.Store
	logger      *zap.Logger
	router      chi.Router
}

// New 创建 API 处理器并初始化 chi 路由
func New(cookieMgr *cookiejar.Manager, tstore *tenant.Store, logger *zap.Logger) *Handler {
	h := &Handler{cookieMgr: cookieMgr, tenantStore: tstore, logger: logger}
	h.router = h.buildRouter()
	return h
}

// buildRouter 构建 /api/v1 路由表
func (h *Handler) buildRouter() chi.Router {
	r := chi.NewRouter()
	r.Post("/api/v1/cookies/read", h.handleCookieRead)
	r.Post("/api/v1/cookies/set", h.handleCookieSet)
	r.Post("/api/v1/cookies/delete", h.handleCookieDelete)
	r.Get("/api/v1/tenant", h.handleTenantInfo)
	r.Post("/api/v1/tenant/auth", h.handleTenantAuth)
	r.Get("/api/v1/tenant/token_status", h.handleTokenStatus)
	return r
}

// ServeHTTP 将 RequestContext 注入请求 context 后交给 chi 路由分发
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request, reqCtx *RequestContext) {
	h.router.ServeHTTP(w, r.WithContext(withRC(r.Context(), reqCtx)))
}
