package internal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zrurf/cifera/internal/addon"
	"github.com/zrurf/cifera/internal/cache"
	"github.com/zrurf/cifera/internal/compress"
	"github.com/zrurf/cifera/internal/constant"
	"github.com/zrurf/cifera/internal/cookiejar"
	"github.com/zrurf/cifera/internal/rewriter"
	"github.com/zrurf/cifera/internal/utils"
	"github.com/zrurf/cifera/internal/vhost"
	"github.com/zrurf/cifera/internal/wsproxy"
	"go.uber.org/zap"
)

// semMaxWait 并发槽位最大等待时长，超过则返回 503，避免请求无限排队
const semMaxWait = 5 * time.Second

// proxyParams 存储在 outbound request context 中的代理参数
type proxyParams struct {
	schema      string // 目标服务器的 scheme
	host        string // 目标服务器的 host
	proxy       string // 代理入口地址，如 "127.0.0.1:8080"
	entryScheme string // 代理入口自身的 scheme
	referer     string // 当前页面原始 URL，转发给源站的 Referer 头
	currentPath string // 当前请求路径（用于相对 URL 解析）
}

type ctxKey string

const (
	proxyParamsKey      ctxKey = "proxy_params"
	fallbackVhostKey    ctxKey = "fallback_vhost"
	cookieJarKey        ctxKey = "cookie_jar"
	cookieSessionIDKey  ctxKey = "cookie_session_id"
	cookieSessionNewKey ctxKey = "cookie_session_new"
	cookieSyncKeysKey   ctxKey = "cookie_sync_keys"
)

// isRedirect 判断响应是否为重定向
func isRedirect(statusCode int) bool {
	return statusCode == http.StatusMovedPermanently ||
		statusCode == http.StatusFound ||
		statusCode == http.StatusTemporaryRedirect ||
		statusCode == http.StatusPermanentRedirect
}

// newOptimizedTransport 创建优化后的 HTTP Transport
func newOptimizedTransport() *http.Transport {
	return &http.Transport{
		// 连接池：加大空闲连接数，减少重复建连
		MaxIdleConns:        500,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     0, // 不限制每主机活跃连接数

		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,

		ForceAttemptHTTP2: true,

		// 禁用自动解压：压缩响应由 ModifyResponse 处理，非 HTML 可透传上游压缩，避免解压再压缩
		DisableCompression: true,

		WriteBufferSize: 256 * 1024,
		ReadBufferSize:  256 * 1024,
	}
}

// CreateServer 创建代理服务器
// addons: 已加载的 addon；registry: 虚拟主机注册表
// negotiator/cch/cookieMgr 可为 nil，分别表示不压缩、不缓存、不启用 Cookie Jar
func CreateServer(logger *zap.Logger, runtimeJS string, addons []*addon.LoadedAddon, registry *vhost.Registry, negotiator *compress.Negotiator, cch *cache.Cache, cookieMgr *cookiejar.Manager) http.Handler {
	h := &ciferaHandler{
		addons:     addons,
		registry:   registry,
		runtimeJS:  runtimeJS,
		logger:     logger,
		negotiator: negotiator,
		cache:      cch,
		cookieMgr:  cookieMgr,
		sem:        make(chan struct{}, 4096), // 最多 4096 个并发代理请求
	}

	transport := newOptimizedTransport()

	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			params, ok := r.In.Context().Value(proxyParamsKey).(*proxyParams)
			if !ok {
				// 兜底：正常流程不会走到这里
				params = parseProxyParams(r.In, logger)
			}

			if params.host == "" {
				logger.Warn("请求缺少目标 host 参数",
					zap.String("path", r.In.URL.Path),
					zap.String("query", r.In.URL.RawQuery),
				)
				return
			}

			outURL := &url.URL{
				Scheme:  params.schema,
				Host:    params.host,
				Path:    r.In.URL.Path,
				RawPath: r.In.URL.RawPath,
			}
			// 仅移除所有 _cifera_* 代理参数
			q := r.In.URL.Query()
			utils.StripMetaParams(q)
			outURL.RawQuery = q.Encode()

			r.Out.URL = outURL
			r.Out.URL.Scheme = params.schema
			r.Out.URL.Host = params.host
			r.Out.Host = params.host
			r.Out.Header.Set("Referer", params.referer)

			// 移除所有 Cifera-* 头，避免泄漏到源服务器
			utils.StripMetaHeaders(r.Out.Header)

			// 移除 _cifera_sid cookie（Cookie Jar 模式已替换为 jar 中的 cookie，此为双重保险）
			if cookieHeader := r.Out.Header.Get("Cookie"); cookieHeader != "" {
				r.Out.Header.Set("Cookie", stripSessionCookie(cookieHeader))
			}

			// 重写 Origin 头为目标站点域名（部分站点会校验 Origin）
			if origin := r.In.Header.Get("Origin"); origin != "" {
				if originURL, parseErr := url.Parse(origin); parseErr == nil {
					originURL.Scheme = params.schema
					originURL.Host = params.host
					r.Out.Header.Set("Origin", originURL.String())
				}
			}

			// 保留客户端 Accept-Encoding：配合 DisableCompression 让上游返回压缩响应
			//（HTML 需解压后改写，非 HTML 直接透传）

			// 将代理参数和 cookie jar 信息存入 context，供 ModifyResponse 使用
			outCtx := r.Out.Context()
			outCtx = context.WithValue(outCtx, proxyParamsKey, params)
			if jar, ok := r.In.Context().Value(cookieJarKey).(*cookiejar.Jar); ok {
				outCtx = context.WithValue(outCtx, cookieJarKey, jar)
			}
			if sid, ok := r.In.Context().Value(cookieSessionIDKey).(string); ok {
				outCtx = context.WithValue(outCtx, cookieSessionIDKey, sid)
			}
			if isNew, ok := r.In.Context().Value(cookieSessionNewKey).(bool); ok {
				outCtx = context.WithValue(outCtx, cookieSessionNewKey, isNew)
			}
			if keys, ok := r.In.Context().Value(cookieSyncKeysKey).([]string); ok {
				outCtx = context.WithValue(outCtx, cookieSyncKeysKey, keys)
			}
			r.Out = r.Out.WithContext(outCtx)
		},
		ModifyResponse: func(resp *http.Response) error {
			params, ok := resp.Request.Context().Value(proxyParamsKey).(*proxyParams)
			if !ok {
				return nil
			}

			// fallback vhost：状态码命中时用 vhost 响应替换
			if vh, ok := resp.Request.Context().Value(fallbackVhostKey).(*vhost.Host); ok && vh != nil {
				if vh.FallbackMatches(resp.StatusCode) {
					logger.Debug("fallback vhost 命中",
						zap.String("vhost", vh.Config.Name),
						zap.Int("status", resp.StatusCode),
					)
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
			// context canceled 是客户端主动断开连接，非服务端错误
			if r.Context().Err() != nil {
				logger.Debug("客户端断开连接，请求取消",
					zap.String("method", r.Method),
					zap.String("path", r.URL.Path),
					zap.Error(err),
				)
				return
			}
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
	proxy      http.Handler
	addons     []*addon.LoadedAddon
	registry   *vhost.Registry
	runtimeJS  string
	logger     *zap.Logger
	negotiator *compress.Negotiator
	cache      *cache.Cache
	cookieMgr  *cookiejar.Manager
	// 并发控制信号量，限制同时处理的代理请求数
	sem chan struct{}
	// addonsMu 保护 addons 字段，支持热加载时的原子替换
	addonsMu sync.RWMutex
}

// loadAddons 返回当前 addon 列表（读锁保护）
func (h *ciferaHandler) loadAddons() []*addon.LoadedAddon {
	h.addonsMu.RLock()
	defer h.addonsMu.RUnlock()
	return h.addons
}

// ReplaceAddons 原子替换 addon 列表（供热加载使用）
func (h *ciferaHandler) ReplaceAddons(addons []*addon.LoadedAddon) {
	h.addonsMu.Lock()
	h.addons = addons
	h.addonsMu.Unlock()
}

// ServeHTTP 处理所有进入的请求
func (h *ciferaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 内置运维端点：不转发到上游
	switch r.URL.Path {
	case "/healthz":
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "ok\n")
		return
	case "/metrics":
		h.writeMetrics(w)
		return
	}

	params := parseProxyParams(r, h.logger)

	// Cookie Jar 会话处理
	if h.cookieMgr != nil && params.host != "" {
		var sessionID string
		if c, err := r.Cookie(constant.CookieSessionID); err == nil {
			sessionID = c.Value
		}

		jar, sid, isNew := h.cookieMgr.GetOrCreateJar(sessionID)

		// 用 jar 中的 cookie 替换请求的 Cookie 头
		jarCookies := jar.Cookies(params.host, r.URL.Path)
		if len(jarCookies) > 0 {
			r.Header.Set("Cookie", buildCookieHeader(jarCookies))
		} else {
			r.Header.Del("Cookie")
		}

		// 解析 Cifera-Cookie-Sync 头：客户端 JS 修改的 cookie 同步到 jar
		var cookieSyncKeys []string
		if syncHeader := r.Header.Get(constant.HeaderCookieSync); syncHeader != "" {
			if syncCookies, err := decodeCookieSyncHeader(syncHeader); err == nil {
				for _, c := range syncCookies {
					jar.AddCookie(c, params.host)
					// 记录 cookie key（name|path）用于 ACK 响应
					cookieSyncKeys = append(cookieSyncKeys, c.Name+"|"+c.Path)
				}
				// 同步的 cookie 一并持久化
				h.cookieMgr.PersistJar(sid, jar)
			}
		}

		// 将 jar 信息存入 context，供 ModifyResponse 使用
		ctx := r.Context()
		ctx = context.WithValue(ctx, cookieJarKey, jar)
		ctx = context.WithValue(ctx, cookieSessionIDKey, sid)
		ctx = context.WithValue(ctx, cookieSessionNewKey, isNew)
		if len(cookieSyncKeys) > 0 {
			ctx = context.WithValue(ctx, cookieSyncKeysKey, cookieSyncKeys)
		}
		r = r.WithContext(ctx)
	}

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

	// WebSocket 升级请求交由 wsproxy 处理
	if wsproxy.IsWebSocketUpgrade(r) && params.host != "" {
		// 优先使用 Cookie Jar 中的 cookie
		cookieStr := r.Header.Get("Cookie")

		// 移除 _cifera_* 参数
		targetPath := r.URL.Path
		q := r.URL.Query()
		utils.StripMetaParams(q)
		if encoded := q.Encode(); encoded != "" {
			targetPath += "?" + encoded
		}

		// 将 http/https 映射为 ws/wss
		wsScheme := params.schema
		switch wsScheme {
		case "http":
			wsScheme = "ws"
		case "https":
			wsScheme = "wss"
		}

		wsproxy.ProxyWebSocket(w, r, wsScheme, params.host, targetPath, params.referer, cookieStr, h.logger)
		return
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

	if params.host == "" {
		http.Error(w, "Missing target host", http.StatusBadRequest)
		return
	}

	// 缓存查找：仅对 GET/HEAD 请求检查缓存
	if h.cache != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		cacheKey := cache.BuildCacheKey(params.schema, params.host, r.URL.RequestURI())
		if cachedResp, ok := h.cache.Get(cacheKey, r); ok {
			h.logger.Debug("缓存命中",
				zap.String("key", cacheKey),
			)
			writeResponse(w, cachedResp)
			return
		}
	}

	// 将 params 存入 context，供 Rewrite 使用
	ctx = context.WithValue(ctx, proxyParamsKey, params)

	// 并发控制：抢占槽位并设置等待上限，避免高负载下请求无限排队
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-ctx.Done():
		h.logger.Debug("等待并发槽位时客户端断开",
			zap.String("path", r.URL.Path),
			zap.Error(ctx.Err()),
		)
		return
	case <-time.After(semMaxWait):
		h.logger.Warn("并发槽位等待超时，返回 503",
			zap.String("path", r.URL.Path),
			zap.Duration("wait", semMaxWait),
		)
		http.Error(w, "Server busy", http.StatusServiceUnavailable)
		return
	}

	h.proxy.ServeHTTP(w, r.WithContext(ctx))
}

// writeMetrics 输出进程内运行指标（当前仅含缓存统计）
func (h *ciferaHandler) writeMetrics(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	var sb strings.Builder
	if h.cache != nil {
		hits, misses, size, count := h.cache.Stats()
		fmt.Fprintf(&sb, "cache_hits_total %d\ncache_misses_total %d\ncache_size_bytes %d\ncache_entries %d\n",
			hits, misses, size, count)
	}
	io.WriteString(w, sb.String())
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

	if err := h.processResponse(resp, params); err != nil {
		h.logger.Error("响应处理失败",
			zap.String("vhost", vh.Config.Name),
			zap.Error(err),
		)
		// 即使处理失败也尝试写回响应
	}

	// override vhost 路径也需压缩
	if h.negotiator != nil {
		if err := h.negotiator.CompressResponse(resp, r); err != nil {
			h.logger.Debug("压缩响应失败，使用未压缩响应",
				zap.Error(err),
			)
		}
	}

	writeResponse(w, resp)
}

// processResponse 处理响应：replace → 重定向 → Cookie → inject → HTML 改写 → 缓存 → 压缩
func (h *ciferaHandler) processResponse(resp *http.Response, params *proxyParams) error {
	// 原始 URL 用于 addon 匹配
	originalURL := buildOriginalURL(params.schema, params.host, resp.Request.URL)

	// addon replace 规则：替换后跳过中间处理步骤，但仍需压缩
	isReplaced := false
	if replaceResult := addon.MatchReplace(h.loadAddons(), originalURL); replaceResult != nil {
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
			isReplaced = true

			h.logger.Debug("addon 替换响应",
				zap.String("addon_id", replaceResult.AddonID),
				zap.String("url", originalURL),
				zap.String("content_type", replaceResult.Rule.ContentType()),
			)

		case addon.ActionReplaceContent:
			// 仅替换响应体，保留原始响应头
			body := replaceResult.Rule.ResourceContent()
			resp.Body = &readCloser{bytes.NewReader(body)}
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			resp.Header.Del("Content-Encoding")
			isReplaced = true

			h.logger.Debug("addon 替换响应体",
				zap.String("addon_id", replaceResult.AddonID),
				zap.String("url", originalURL),
			)
		}
	}

	if !isReplaced {
		// 重写重定向 Location
		if isRedirect(resp.StatusCode) {
			rewriteRedirectLocation(resp, params)
			h.logger.Debug("重定向响应",
				zap.Int("status", resp.StatusCode),
				zap.String("location", resp.Header.Get("Location")),
			)
		}

		// Cookie Jar：拦截 Set-Cookie，存入 jar，从响应中移除
		if h.cookieMgr != nil {
			if jar, ok := resp.Request.Context().Value(cookieJarKey).(*cookiejar.Jar); ok {
				setCookies := extractSetCookies(resp)
				if len(setCookies) > 0 {
					for _, c := range setCookies {
						jar.AddCookie(c, params.host)
					}
					if sid, sidOk := resp.Request.Context().Value(cookieSessionIDKey).(string); sidOk {
						h.cookieMgr.PersistJar(sid, jar)
					}
					// 通过 Cifera-Cookie-Push 头增量推送 Set-Cookie 变更（格式与 Cifera-Cookie-Sync 一致），客户端 JS 据此更新 Shadow Jar
					resp.Header.Set(constant.HeaderCookiePush, buildCookiePushHeader(setCookies))
				}
				// 新会话：在响应中设置 _cifera_sid cookie（由 ReverseProxy 写回客户端）
				if isNew, ok := resp.Request.Context().Value(cookieSessionNewKey).(bool); ok && isNew {
					if sid, sidOk := resp.Request.Context().Value(cookieSessionIDKey).(string); sidOk {
						cookie := &http.Cookie{
							Name:     constant.CookieSessionID,
							Value:    sid,
							Path:     "/",
							HttpOnly: true,
							SameSite: http.SameSiteLaxMode,
						}
						resp.Header.Add("Set-Cookie", cookie.String())
					}
				}
			}
		} else {
			// Cookie Jar 未启用：降级为原有 Set-Cookie 重写
			rewriteCookiesLegacy(resp, params)
		}

		// Cifera-Cookie-Ack：确认客户端 cookie 同步成功
		if keys, ok := resp.Request.Context().Value(cookieSyncKeysKey).([]string); ok && len(keys) > 0 {
			resp.Header.Set(constant.HeaderCookieAck, strings.Join(keys, ","))
		}

		injectItems := addon.MatchInject(h.addons, originalURL)

		contentType := strings.ToLower(resp.Header.Get("Content-Type"))
		isHTML := strings.Contains(contentType, "text/html")

		// 处理上游压缩的 HTML：需解压后才能改写
		contentEncoding := resp.Header.Get("Content-Encoding")
		if isHTML && contentEncoding != "" {
			if err := decompressResponseBody(resp, contentEncoding); err != nil {
				h.logger.Error("解压上游 HTML 响应失败",
					zap.String("host", params.host),
					zap.String("path", params.currentPath),
					zap.String("encoding", contentEncoding),
					zap.Error(err),
				)
				return err
			}
		}

		// 非 HTML 压缩响应：透传，跳过改写与压缩
		if !isHTML && contentEncoding != "" {
			h.logger.Debug("透传上游压缩响应",
				zap.String("host", params.host),
				zap.String("path", params.currentPath),
				zap.String("encoding", contentEncoding),
			)
			// 不缓存压缩响应：缓存统一未压缩版本，兼容不同客户端算法
			return nil
		}

		// 改写 HTML body
		proxyBase := params.entryScheme + "://" + params.proxy
		if isHTML {
			// 将当前 host 的 jar cookie 序列化为 JSON，供改写注入页面
			var cookiesJSON string
			if h.cookieMgr != nil {
				if jar, ok := resp.Request.Context().Value(cookieJarKey).(*cookiejar.Jar); ok {
					jarCookies := jar.Cookies(params.host, params.currentPath)
					if len(jarCookies) > 0 {
						cookiesJSON = buildCookiesJSON(jarCookies)
					}
				}
			}
			if err := rewriter.RewriteResponse(resp, h.runtimeJS, proxyBase, params.currentPath, params.host, params.schema, params.referer, cookiesJSON, injectItems, h.logger); err != nil {
				h.logger.Error("改写 HTML 响应失败",
					zap.String("host", params.host),
					zap.String("path", params.currentPath),
					zap.Error(err),
				)
				return err
			}
		}

		// 缓存：异步存储非 HTML、可缓存的响应（压缩前缓存，确保缓存通用性）
		if h.cache != nil && !isHTML {
			req := resp.Request
			if req != nil && cache.IsCacheable(resp, req) {
				body, err := io.ReadAll(resp.Body)
				if err == nil {
					cacheKey := cache.BuildCacheKey(params.schema, params.host, req.URL.RequestURI())
					// 异步写入缓存，不阻塞响应
					h.cache.SetAsync(cacheKey, resp.Header, body, resp.StatusCode)
					h.logger.Debug("缓存异步存储",
						zap.String("key", cacheKey),
						zap.Int("size", len(body)),
					)
					// 读完后重置 body，供后续压缩和写入
					resp.Body = io.NopCloser(bytes.NewReader(body))
				}
			}
		}
	}

	// 压缩响应（替换后的响应也需压缩；透传分支已提前返回）
	if h.negotiator != nil {
		origReq := resp.Request
		if origReq != nil {
			if err := h.negotiator.CompressResponse(resp, origReq); err != nil {
				h.logger.Debug("压缩响应失败，使用未压缩响应",
					zap.Error(err),
				)
			}
		}
	}

	return nil
}

// decompressResponseBody 解压上游压缩的响应体
func decompressResponseBody(resp *http.Response, encoding string) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	resp.Body.Close()

	decoded, err := compress.Decode(body, encoding)
	if err != nil {
		return err
	}

	resp.Body = io.NopCloser(bytes.NewReader(decoded))
	resp.ContentLength = int64(len(decoded))
	resp.Header.Set("Content-Length", strconv.Itoa(len(decoded)))
	resp.Header.Del("Content-Encoding")
	return nil
}

// parseProxyParams 从请求中解析代理参数
// 解析顺序：URL 查询参数 → Cifera-Referer 头 → Referer 头补充 → r.Host 兜底
func parseProxyParams(r *http.Request, logger *zap.Logger) *proxyParams {
	_, schema, host := utils.ParseProxyUrl(r.URL)

	// 从 Cifera-Referer 头获取 referer（当前页面的原始 URL）
	referer := r.Header.Get(constant.HeaderReferer)

	// 从 Referer 头补充缺失的参数，并尝试重构原始 URL 作为 referer
	if headerRef := r.Header.Get("Referer"); headerRef != "" {
		if refURL, parseErr := url.Parse(headerRef); parseErr == nil {
			_, parsedSchema, parsedHost := utils.ParseProxyUrl(refURL)
			if host == "" {
				host = parsedHost
			}
			if schema == "" {
				schema = parsedSchema
			}
			// Cifera-Referer 为空时，从浏览器自动发送的 Referer 头（代理 URL 格式）
			// 中提取 _cifera_h/_cifera_s，重构源站原始 URL
			if referer == "" && parsedHost != "" {
				refURL.Scheme = parsedSchema
				refURL.Host = parsedHost
				q := refURL.Query()
				utils.StripMetaParams(q)
				refURL.RawQuery = q.Encode()
				referer = refURL.String()
			}
		}
	}

	// host 仍为空时使用 r.Host（直接访问 vhost 场景）
	if host == "" && r.Host != "" {
		host = r.Host
		if schema == "" {
			schema = "http"
			if r.TLS != nil {
				schema = "https"
			}
		}
	}

	// 剥离源站 host 中误拼接的代理端口（详见 stripProxyPort）
	host = stripProxyPort(host, r.Host, logger)

	if schema == "" {
		schema = "http"
	}

	currentPath := r.URL.Path

	entryScheme := "http"
	if r.TLS != nil {
		entryScheme = "https"
	}

	return &proxyParams{
		schema:      schema,
		host:        host,
		proxy:       r.Host,
		entryScheme: entryScheme,
		referer:     referer,
		currentPath: currentPath,
	}
}

// writeResponse 将 *http.Response 写回 http.ResponseWriter
func writeResponse(w http.ResponseWriter, resp *http.Response) {
	// 复制 Header（跳过 hop-by-hop 头）
	hopByHop := map[string]bool{
		"Connection":          true,
		"Proxy-Connection":    true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"Te":                  true,
		"Trailer":             true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
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
	// 仅移除所有 _cifera_* 代理参数
	q := reqURL.Query()
	utils.StripMetaParams(q)
	u.RawQuery = q.Encode()
	return u.String()
}

// stripProxyPort 剥离源站 host 中误拼接的代理端口
// 前端 JS 可能把代理端口拼进源站 host（如 "cn.bing.com:8080"）；
// 仅当 host 尾部端口等于代理端口且 hostname 不是代理 hostname（或其子域）时剥离，
// 否则端口可能是源站合法端口或代理 host 自身端口，需保留。
func stripProxyPort(host, requestHost string, logger *zap.Logger) string {
	if host == "" || requestHost == "" {
		return host
	}

	var proxyHostname, proxyPort string
	if h, p, err := net.SplitHostPort(requestHost); err == nil {
		proxyHostname = h
		proxyPort = p
	} else {
		// requestHost 不含端口，无需处理
		return host
	}

	if proxyPort == "" {
		return host
	}

	hostHostname, hostPort, err := net.SplitHostPort(host)
	if err != nil {
		// host 不含端口，无需处理
		return host
	}

	if hostPort != proxyPort {
		// 端口不等于代理端口，可能是源站合法端口，保留
		return host
	}

	// 端口等于代理端口：代理 hostname 或其子域需保留端口，其余视为误拼接而剥离
	if hostHostname == proxyHostname || strings.HasSuffix(hostHostname, "."+proxyHostname) {
		return host
	}

	logger.Debug("剥离源站 host 中意外携带的代理端口",
		zap.String("original_host", host),
		zap.String("stripped_host", hostHostname),
		zap.String("proxy_port", proxyPort),
	)

	return hostHostname
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
		locURL.RawQuery = q.Encode()
	} else {
		q := locURL.Query()
		q.Set("_cifera_h", params.host)
		if params.schema != "" && params.schema != "http" {
			q.Set("_cifera_s", params.schema)
		}
		locURL.RawQuery = q.Encode()
	}

	resp.Header.Set("Location", locURL.String())
}

// rewriteCookiesLegacy 重写 Set-Cookie 响应头（Cookie Jar 未启用时的降级行为）
func rewriteCookiesLegacy(resp *http.Response, params *proxyParams) {
	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		return
	}

	var rewritten []string
	for _, cookie := range cookies {
		rewritten = append(rewritten, rewriteCookieString(cookie))
	}

	resp.Header.Del("Set-Cookie")

	for _, c := range rewritten {
		resp.Header.Add("Set-Cookie", c)
	}
}

// rewriteCookieString 重写单个 Set-Cookie 头值（移除 Domain 属性）
func rewriteCookieString(cookie string) string {
	parts := strings.Split(cookie, ";")
	var kept []string

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		lower := strings.ToLower(trimmed)

		if strings.HasPrefix(lower, "domain=") {
			continue
		}

		kept = append(kept, part)
	}

	return strings.Join(kept, ";")
}

// stripSessionCookie 从 Cookie 头中移除 _cifera_sid cookie
func stripSessionCookie(cookieHeader string) string {
	parts := strings.Split(cookieHeader, ";")
	var kept []string
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if strings.HasPrefix(trimmed, constant.CookieSessionID+"=") {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return ""
	}
	return strings.Join(kept, ";")
}
