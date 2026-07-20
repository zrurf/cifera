package internal

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"

	"github.com/zrurf/cifera/internal/addon"
	"github.com/zrurf/cifera/internal/constant"
	"github.com/zrurf/cifera/internal/rewriter"
	"github.com/zrurf/cifera/internal/utils"
	"github.com/zrurf/cifera/internal/vhost"
	"go.uber.org/zap"
)

// proxyParams 存储在 outbound request context 中的代理参数
type proxyParams struct {
	schema      string // 目标服务器的 scheme
	host        string // 目标服务器的 host
	proxy       string // 代理入口地址，如 "127.0.0.1:8080"
	entryScheme string // 代理入口自身的 scheme（http/https）
	referer     string // 当前请求的来源页（用于 Referer 头）
	pageOrigin  string // 当前资源的原始 URL（用于 _cifera_r，即子资源的来源标识）
	currentPath string // 当前请求的路径（用于相对 URL 解析）
}

type ctxKey string

const (
	proxyParamsKey   ctxKey = "proxy_params"
	fallbackVhostKey ctxKey = "fallback_vhost"
)

// isRedirect 判断响应是否为重定向
func isRedirect(statusCode int) bool {
	return statusCode == http.StatusMovedPermanently ||
		statusCode == http.StatusFound ||
		statusCode == http.StatusTemporaryRedirect ||
		statusCode == http.StatusPermanentRedirect
}

// CreateServer 创建代理服务器
// addons: 已加载的 addon 列表
// registry: 虚拟主机注册表
func CreateServer(logger *zap.Logger, runtimeJS string, addons []*addon.LoadedAddon, registry *vhost.Registry) http.Handler {
	h := &ciferaHandler{
		addons:   addons,
		registry: registry,
		runtimeJS: runtimeJS,
		logger:   logger,
	}

	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			params, ok := r.In.Context().Value(proxyParamsKey).(*proxyParams)
			if !ok {
				// 兜底：从请求中解析参数（正常流程不会走到这里）
				params = parseProxyParams(r.In, logger)
			}

			// 未能确定目标 host，无法代理
			if params.host == "" {
				logger.Warn("请求缺少目标 host 参数",
					zap.String("path", r.In.URL.Path),
					zap.String("query", r.In.URL.RawQuery),
				)
				return
			}

			// 构建目标 URL
			outURL := &url.URL{
				Scheme:  params.schema,
				Host:    params.host,
				Path:    r.In.URL.Path,
				RawPath: r.In.URL.RawPath,
			}
			// 复制查询参数，移除代理参数
			q := r.In.URL.Query()
			q.Del(constant.ProxyHostPrefix)
			q.Del(constant.ProxySchemaPrefix)
			q.Del(constant.ProxyRefererPrefix)
			outURL.RawQuery = q.Encode()

			r.Out.URL = outURL
			r.Out.URL.Scheme = params.schema
			r.Out.URL.Host = params.host
			r.Out.Host = params.host
			r.Out.Header.Set("Referer", params.referer)

			// 重写 Origin 头：将代理域名替换为目标站点域名
			// 部分站点校验 Origin，必须使用目标站点的域名
			if origin := r.In.Header.Get("Origin"); origin != "" {
				if originURL, parseErr := url.Parse(origin); parseErr == nil {
					originURL.Scheme = params.schema
					originURL.Host = params.host
					r.Out.Header.Set("Origin", originURL.String())
				}
			}

			// 删除 Accept-Encoding，让上游返回未压缩的 HTML，便于改写
			r.Out.Header.Del("Accept-Encoding")

			// 将代理参数存入 context，供 ModifyResponse 使用
			r.Out = r.Out.WithContext(context.WithValue(r.Out.Context(), proxyParamsKey, params))
		},
		ModifyResponse: func(resp *http.Response) error {
			params, ok := resp.Request.Context().Value(proxyParamsKey).(*proxyParams)
			if !ok {
				return nil
			}

			// fallback vhost 检查：状态码命中正则时替换为 vhost 响应
			if vh, ok := resp.Request.Context().Value(fallbackVhostKey).(*vhost.Host); ok && vh != nil {
				if vh.FallbackMatches(resp.StatusCode) {
					logger.Debug("fallback vhost 命中",
						zap.String("vhost", vh.Config.Name),
						zap.Int("status", resp.StatusCode),
					)
					// 用 vhost 响应替换原响应
					newResp, err := vh.Serve(resp.Request)
					if err == nil && newResp != nil {
						resp.Body.Close()
						resp = newResp
						logger.Info("fallback vhost 提供响应",
							zap.String("vhost", vh.Config.Name),
							zap.String("type", string(vh.Config.Type)),
							zap.Int("status", resp.StatusCode),
						)
					} else if err != nil {
						logger.Warn("fallback vhost 服务失败，使用原始响应",
							zap.String("vhost", vh.Config.Name),
							zap.Error(err),
						)
					}
				}
			}

			return h.processResponse(resp, params)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			logger.Error("代理请求失败",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Error(err),
			)
			http.Error(w, "代理请求失败", http.StatusBadGateway)
		},
	}

	h.proxy = proxy
	return h
}

// ciferaHandler 整合 addon block 检查、虚拟主机分发和响应处理的 handler
type ciferaHandler struct {
	proxy     http.Handler
	addons    []*addon.LoadedAddon
	registry  *vhost.Registry
	runtimeJS string
	logger    *zap.Logger
}

// ServeHTTP 处理所有进入的请求
func (h *ciferaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 解析代理参数
	params := parseProxyParams(r, h.logger)

	// addon block 检查
	if params.host != "" {
		originalURL := buildOriginalURL(params.schema, params.host, r.URL)
		if blockResult := addon.MatchBlock(h.addons, originalURL); blockResult != nil {
			h.logger.Info("addon 拦截请求",
				zap.String("addon_id", blockResult.AddonID),
				zap.String("url", originalURL),
				zap.Int("status_code", blockResult.Rule.StatusCode),
			)
			w.WriteHeader(blockResult.Rule.StatusCode)
			return
		}
	}

	// 虚拟主机查找
	vh := h.registry.Lookup(params.host, r.Host)

	if vh != nil && vh.Config.Priority == vhost.PriorityOverride {
		// override 模式：直接由 vhost 服务
		h.serveOverride(w, r, vh, params)
		return
	}

	// fallback 或无 vhost：转发到代理
	ctx := r.Context()
	if vh != nil && vh.Config.Priority == vhost.PriorityFallback {
		// 将 fallback vhost 存入 context，供 ModifyResponse 使用
		ctx = context.WithValue(ctx, fallbackVhostKey, vh)
	}

	// 无目标 host 且无 vhost，返回 400
	if params.host == "" {
		http.Error(w, "Missing target host", http.StatusBadRequest)
		return
	}

	// 将 params 存入 context，供 Rewrite 使用
	ctx = context.WithValue(ctx, proxyParamsKey, params)
	h.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// serveOverride 用 override vhost 服务请求
func (h *ciferaHandler) serveOverride(w http.ResponseWriter, r *http.Request, vh *vhost.Host, params *proxyParams) {
	resp, err := vh.Serve(r)
	if err != nil {
		h.logger.Error("override vhost 服务失败",
			zap.String("vhost", vh.Config.Name),
			zap.Error(err),
		)
		http.Error(w, "VHost serve error", http.StatusBadGateway)
		return
	}

	// 确保 resp 关联请求（processResponse 中可能需要 resp.Request）
	if resp.Request == nil {
		resp.Request = r
	}

	// 应用 addon 规则 + HTML 改写
	if err := h.processResponse(resp, params); err != nil {
		h.logger.Error("响应处理失败",
			zap.String("vhost", vh.Config.Name),
			zap.Error(err),
		)
		// 即使处理失败也尝试写回响应
	}

	// 写回客户端
	writeResponse(w, resp)
}

// processResponse 处理响应：addon replace → redirect → cookie → inject → HTML rewrite
func (h *ciferaHandler) processResponse(resp *http.Response, params *proxyParams) error {
	// 构造原始 URL 用于 addon 匹配
	originalURL := buildOriginalURL(params.schema, params.host, resp.Request.URL)

	// 处理 addon replace / replace_content 规则
	if replaceResult := addon.MatchReplace(h.addons, originalURL); replaceResult != nil {
		switch replaceResult.Rule.Action {
		case addon.ActionReplace:
			// 替换整个响应（body + Content-Type）
			body := replaceResult.Rule.ResourceContent()
			resp.Body = &readCloser{bytes.NewReader(body)}
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			resp.Header.Set("Content-Type", replaceResult.Rule.ContentType())
			resp.Header.Del("Content-Encoding")
			resp.StatusCode = http.StatusOK
			resp.Status = http.StatusText(http.StatusOK)

			h.logger.Debug("addon 替换响应",
				zap.String("addon_id", replaceResult.AddonID),
				zap.String("url", originalURL),
				zap.String("content_type", replaceResult.Rule.ContentType()),
			)
			return nil

		case addon.ActionReplaceContent:
			// 仅替换响应体，保留原始响应头
			body := replaceResult.Rule.ResourceContent()
			resp.Body = &readCloser{bytes.NewReader(body)}
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			resp.Header.Del("Content-Encoding")

			h.logger.Debug("addon 替换响应体",
				zap.String("addon_id", replaceResult.AddonID),
				zap.String("url", originalURL),
			)
			return nil
		}
	}

	// 处理重定向 Location
	if isRedirect(resp.StatusCode) {
		rewriteRedirectLocation(resp, params)
		h.logger.Debug("重定向响应",
			zap.Int("status", resp.StatusCode),
			zap.String("location", resp.Header.Get("Location")),
		)
	}

	// 处理 Set-Cookie：重写 Domain 和 Path
	rewriteCookies(resp, params)

	// 匹配 addon inject 规则
	injectItems := addon.MatchInject(h.addons, originalURL)

	// 处理 HTML 响应 body 改写
	proxyBase := params.entryScheme + "://" + params.proxy
	if err := rewriter.RewriteResponse(resp, h.runtimeJS, proxyBase, params.currentPath, params.host, params.schema, params.referer, params.pageOrigin, injectItems, h.logger); err != nil {
		h.logger.Error("改写 HTML 响应失败",
			zap.String("host", params.host),
			zap.String("path", params.currentPath),
			zap.Error(err),
		)
		return err
	}

	return nil
}

// parseProxyParams 从请求中解析代理参数
// 解析顺序：URL 查询参数 → Referer 头补充 → r.Host 兜底
func parseProxyParams(r *http.Request, logger *zap.Logger) *proxyParams {
	_, schema, host, referer := utils.ParseProxyUrl(r.URL)

	// 从 Referer 中补充缺失的参数
	if headerRef := r.Header.Get("Referer"); headerRef != "" {
		if refURL, parseErr := url.Parse(headerRef); parseErr == nil {
			parsedOrigin, parsedSchema, parsedHost, _ := utils.ParseProxyUrl(refURL)
			if referer == "" {
				referer = parsedOrigin
			}
			if host == "" {
				host = parsedHost
			}
			if schema == "" {
				schema = parsedSchema
			}
		}
	}

	// 如果 host 仍为空，使用 r.Host（直接访问 vhost 场景）
	if host == "" && r.Host != "" {
		host = r.Host
		if schema == "" {
			schema = "http"
			if r.TLS != nil {
				schema = "https"
			}
		}
	}

	// schema 默认值
	if schema == "" {
		schema = "http"
	}

	// 当前请求的路径（不含代理参数）
	currentPath := r.URL.Path

	// 代理入口自身的 scheme
	entryScheme := "http"
	if r.TLS != nil {
		entryScheme = "https"
	}

	// 计算 pageOrigin：当前资源的原始 URL（去掉 _cifera_* 参数后）
	// 用于 _cifera_r 参数，告知子请求的来源页
	pageOrigin := ""
	if host != "" {
		pageOriginURL := &url.URL{
			Scheme:  schema,
			Host:    host,
			Path:    r.URL.Path,
			RawPath: r.URL.RawPath,
		}
		q := r.URL.Query()
		q.Del(constant.ProxyHostPrefix)
		q.Del(constant.ProxySchemaPrefix)
		q.Del(constant.ProxyRefererPrefix)
		pageOriginURL.RawQuery = q.Encode()
		pageOrigin = pageOriginURL.String()
	}

	return &proxyParams{
		schema:      schema,
		host:        host,
		proxy:       r.Host,
		entryScheme: entryScheme,
		referer:     referer,
		pageOrigin:  pageOrigin,
		currentPath: currentPath,
	}
}

// writeResponse 将 *http.Response 写回 http.ResponseWriter
func writeResponse(w http.ResponseWriter, resp *http.Response) {
	// 复制 Header（跳过 hop-by-hop 头）
	hopByHop := map[string]bool{
		"Connection":        true,
		"Proxy-Connection":  true,
		"Keep-Alive":        true,
		"Proxy-Authenticate": true,
		"Proxy-Authorization": true,
		"Te":                true,
		"Trailer":           true,
		"Transfer-Encoding": true,
		"Upgrade":           true,
	}

	for key, vals := range resp.Header {
		if hopByHop[key] {
			continue
		}
		for _, v := range vals {
			w.Header().Add(key, v)
		}
	}

	w.WriteHeader(resp.StatusCode)

	if resp.Body != nil {
		io.Copy(w, resp.Body)
		resp.Body.Close()
	}
}

// buildOriginalURL 构造原始 URL（从代理参数还原）
func buildOriginalURL(schema, host string, reqURL *url.URL) string {
	u := &url.URL{
		Scheme: schema,
		Host:   host,
		Path:   reqURL.Path,
	}
	// 复制查询参数，移除代理参数
	q := reqURL.Query()
	q.Del(constant.ProxyHostPrefix)
	q.Del(constant.ProxySchemaPrefix)
	q.Del(constant.ProxyRefererPrefix)
	u.RawQuery = q.Encode()
	return u.String()
}

// readCloser 包装 io.Reader 为 io.ReadCloser
type readCloser struct {
	*bytes.Reader
}

func (rc *readCloser) Close() error { return nil }

// rewriteRedirectLocation 重写重定向响应的 Location 头
func rewriteRedirectLocation(resp *http.Response, params *proxyParams) {
	location := resp.Header.Get("Location")
	if location == "" {
		return
	}

	locURL, err := url.Parse(location)
	if err != nil {
		return
	}

	proxyScheme := "http"
	if params.entryScheme != "" {
		proxyScheme = params.entryScheme
	}

	if locURL.Host != "" {
		targetHost := locURL.Host
		targetScheme := locURL.Scheme

		locURL.Scheme = proxyScheme
		locURL.Host = params.proxy
		q := locURL.Query()
		q.Set("_cifera_h", targetHost)
		if targetScheme != "" && targetScheme != "http" {
			q.Set("_cifera_s", targetScheme)
		}
		if params.pageOrigin != "" {
			q.Set("_cifera_r", params.pageOrigin)
		}
		locURL.RawQuery = q.Encode()
	} else {
		q := locURL.Query()
		q.Set("_cifera_h", params.host)
		if params.schema != "" && params.schema != "http" {
			q.Set("_cifera_s", params.schema)
		}
		if params.pageOrigin != "" {
			q.Set("_cifera_r", params.pageOrigin)
		}
		locURL.RawQuery = q.Encode()
	}

	resp.Header.Set("Location", locURL.String())
}

// rewriteCookies 重写 Set-Cookie 响应头
// 移除 Domain 属性（或改为代理域名），重写 Path 以适配代理路径
func rewriteCookies(resp *http.Response, params *proxyParams) {
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		return
	}

	var rewritten []string
	for _, cookie := range cookies {
		rewritten = append(rewritten, rewriteCookieString(cookie))
	}

	// 清除原有的 Set-Cookie 头
	resp.Header.Del("Set-Cookie")

	// 设置改写后的 Set-Cookie 头
	for _, c := range rewritten {
		resp.Header.Add("Set-Cookie", c)
	}
}
