package internal

import (
	"bytes"
	"context"
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

// newOptimizedTransport 创建优化后的 HTTP Transport
func newOptimizedTransport() *http.Transport {
	return &http.Transport{
		// 连接池优化：大幅增加每主机空闲连接数
		MaxIdleConns:        500,
		MaxIdleConnsPerHost: 100,
		MaxConnsPerHost:     0, // 不限制每主机活跃连接数

		// 超时设置
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,

		// 启用 HTTP/2
		ForceAttemptHTTP2: true,

		// 禁用自动解压：由我们在 ModifyResponse 中处理
		// 这样非 HTML 响应可以透传上游压缩，避免解压-再压缩的开销
		DisableCompression: true,

		// 缓冲优化
		WriteBufferSize: 256 * 1024, // 256KB 写缓冲
		ReadBufferSize:  256 * 1024, // 256KB 读缓冲
	}
}

// CreateServer 创建代理服务器
// addons: 已加载的 addon 列表
// registry: 虚拟主机注册表
// negotiator: 压缩协商器（可为 nil 表示不压缩）
// cch: 缓存实例（可为 nil 表示不缓存）
func CreateServer(logger *zap.Logger, runtimeJS string, addons []*addon.LoadedAddon, registry *vhost.Registry, negotiator *compress.Negotiator, cch *cache.Cache) http.Handler {
	h := &ciferaHandler{
		addons:     addons,
		registry:   registry,
		runtimeJS:  runtimeJS,
		logger:     logger,
		negotiator: negotiator,
		cache:      cch,
		sem:        make(chan struct{}, 4096), // 最多 4096 个并发代理请求
	}

	transport := newOptimizedTransport()

	proxy := &httputil.ReverseProxy{
		Transport: transport,
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

			// 转发客户端 Accept-Encoding 给上游
			// 配合 DisableCompression: true，上游可能返回压缩响应
			// 非 HTML 压缩响应可透传给客户端，HTML 则需解压后改写再压缩
			// 不再无条件删除 Accept-Encoding

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
	proxy      http.Handler
	addons     []*addon.LoadedAddon
	registry   *vhost.Registry
	runtimeJS  string
	logger     *zap.Logger
	negotiator *compress.Negotiator
	cache      *cache.Cache
	// 并发控制信号量，限制同时处理的代理请求数
	sem chan struct{}
	// addon 规则匹配结果缓存（正则匹配开销大，短生命周期缓存减少重复计算）
	addonMatchCache sync.Map
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

	// 缓存查找：仅对 GET/HEAD 请求检查缓存
	if h.cache != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		cacheKey := cache.BuildCacheKey(params.schema, params.host, r.URL.RequestURI())
		if cachedResp, ok := h.cache.Get(cacheKey); ok {
			h.logger.Debug("缓存命中",
				zap.String("key", cacheKey),
			)
			// 写回缓存的响应
			writeResponse(w, cachedResp)
			return
		}
	}

	// 将 params 存入 context，供 Rewrite 使用
	ctx = context.WithValue(ctx, proxyParamsKey, params)

	// 并发控制：获取信号量，排队等待而非直接拒绝
	// 当并发数达到上限时，请求会阻塞等待槽位释放，而非返回 503
	// 若客户端在等待期间断开连接，通过 context 取消避免 goroutine 泄漏
	select {
	case h.sem <- struct{}{}:
		defer func() { <-h.sem }()
	case <-ctx.Done():
		h.logger.Debug("等待并发槽位时客户端断开",
			zap.String("path", r.URL.Path),
			zap.Error(ctx.Err()),
		)
		return
	}

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

	// 压缩响应（override vhost 路径也需要压缩）
	if h.negotiator != nil {
		if err := h.negotiator.CompressResponse(resp, r); err != nil {
			h.logger.Debug("压缩响应失败，使用未压缩响应",
				zap.Error(err),
			)
		}
	}

	// 写回客户端
	writeResponse(w, resp)
}

// processResponse 处理响应：addon replace → redirect → cookie → inject → HTML rewrite → cache → compress
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

	// 检测 Content-Type 是否为 HTML
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	isHTML := strings.Contains(contentType, "text/html")

	// 处理上游压缩的 HTML：需要解压后才能改写
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

	// 非 HTML 且上游已压缩：透传压缩响应（无需解压和改写）
	// 直接跳过 HTML 改写和代理层压缩
	if !isHTML && contentEncoding != "" {
		h.logger.Debug("透传上游压缩响应",
			zap.String("host", params.host),
			zap.String("path", params.currentPath),
			zap.String("encoding", contentEncoding),
		)
		// 缓存：不缓存已压缩响应（不同客户端支持不同算法，缓存统一未压缩版本更灵活）
		return nil
	}

	// 处理 HTML 响应 body 改写
	proxyBase := params.entryScheme + "://" + params.proxy
	if isHTML {
		if err := rewriter.RewriteResponse(resp, h.runtimeJS, proxyBase, params.currentPath, params.host, params.schema, params.referer, params.pageOrigin, injectItems, h.logger); err != nil {
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
			// 读取 body 用于缓存
			body, err := io.ReadAll(resp.Body)
			if err == nil {
				cacheKey := cache.BuildCacheKey(params.schema, params.host, req.URL.RequestURI())
				// 异步写入缓存，不阻塞响应
				h.cache.SetAsync(cacheKey, resp.Header, body, resp.StatusCode)
				h.logger.Debug("缓存异步存储",
					zap.String("key", cacheKey),
					zap.Int("size", len(body)),
				)
				// 重置 body 供后续压缩和写入
				resp.Body = io.NopCloser(bytes.NewReader(body))
			}
		}
	}

	// 压缩响应（非透传的情况下才压缩）
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

	// 剥离源站 host 中意外携带的代理端口
	// 前端 JS 可能将 window.location.port（代理端口）拼接到源站 host 上，
	// 产生如 "cn.bing.com:8080" 的错误 host（源站实际不在 8080 端口）。
	// 检测逻辑：host 末尾的端口等于代理端口，且 host 去掉端口后的部分
	// 不是代理 hostname（避免误剥离代理 host 本身的端口）
	host = stripProxyPort(host, r.Host, logger)

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
	// 复制查询参数，移除代理参数
	q := reqURL.Query()
	q.Del(constant.ProxyHostPrefix)
	q.Del(constant.ProxySchemaPrefix)
	q.Del(constant.ProxyRefererPrefix)
	u.RawQuery = q.Encode()
	return u.String()
}

// stripProxyPort 剥离源站 host 中意外携带的代理端口
// 场景：前端 JS 将 window.location.port（代理端口）拼接到源站 host 上，
// 产生如 "cn.bing.com:8080" 的错误 host。
//
// 参数：
//   - host: 从 _cifera_h 参数提取的目标 host
//   - requestHost: 请求的 r.Host，即代理入口地址（如 "127.0.0.1:8080"）
//
// 逻辑：
//  1. 从 requestHost 中提取代理端口
//  2. 如果 host 以 ":proxyPort" 结尾，且去掉端口后的 hostname 不是代理 hostname，
//     则剥离端口（该端口是代理端口而非源站端口）
//  3. 如果 hostname 是代理 hostname 本身，保留端口（代理 host 本身就需要带端口）
func stripProxyPort(host, requestHost string, logger *zap.Logger) string {
	if host == "" || requestHost == "" {
		return host
	}

	// 从代理入口地址提取 hostname 和 port
	var proxyHostname, proxyPort string
	if h, p, err := net.SplitHostPort(requestHost); err == nil {
		proxyHostname = h
		proxyPort = p
	} else {
		// requestHost 不含端口（如 "127.0.0.1"），无需处理
		return host
	}

	if proxyPort == "" {
		return host
	}

	// 检查 host 是否以 ":proxyPort" 结尾
	hostHostname, hostPort, err := net.SplitHostPort(host)
	if err != nil {
		// host 不含端口，无需处理
		return host
	}

	if hostPort != proxyPort {
		// host 的端口不等于代理端口，可能是源站的合法端口，保留
		return host
	}

	// host 的端口等于代理端口：
	// - 如果 hostHostname 是代理 hostname → 保留（代理 host 本身需要带端口）
	// - 如果 hostHostname 是代理 hostname 的子域 → 保留（子域代理场景也需要端口）
	// - 其他情况 → 剥离端口（源站 host 被意外拼接了代理端口）
	if hostHostname == proxyHostname || strings.HasSuffix(hostHostname, "."+proxyHostname) {
		// 代理 host 相关，保留端口
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
