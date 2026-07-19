package internal

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/zrurf/cifera/internal/constant"
	"github.com/zrurf/cifera/internal/rewriter"
	"github.com/zrurf/cifera/internal/utils"
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

const proxyParamsKey ctxKey = "proxy_params"

// isRedirect 判断响应是否为重定向
func isRedirect(statusCode int) bool {
	return statusCode == http.StatusMovedPermanently ||
		statusCode == http.StatusFound ||
		statusCode == http.StatusTemporaryRedirect ||
		statusCode == http.StatusPermanentRedirect
}

func CreateServer(logger *zap.Logger, runtimeJS string) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			origin, schema, host, referer := utils.ParseProxyUrl(r.In.URL)

			if headerRef := r.In.Header.Get("Referer"); headerRef != "" {
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

			// 未能确定目标 host，无法代理
			if host == "" {
				logger.Warn("请求缺少目标 host 参数",
					zap.String("path", r.In.URL.Path),
					zap.String("query", r.In.URL.RawQuery),
				)
				return
			}

			outURL, err := url.Parse(origin)
			if err != nil {
				logger.Error("解析代理 URL 失败",
					zap.String("origin", origin),
					zap.Error(err),
				)
				return
			}

			// 当前请求的路径（不含代理参数）
			currentPath := r.In.URL.Path
			r.Out.URL = outURL
			r.Out.URL.Scheme = schema
			r.Out.URL.Host = host
			r.Out.Host = host
			r.Out.Header.Set("Referer", referer)

			// 计算 pageOrigin：当前资源的原始 URL（去掉 _cifera_* 参数后）
			// 用于 _cifera_r 参数，告知子请求的来源页
			pageOrigin := ""
			if host != "" {
				pageOriginURL := &url.URL{
					Scheme:  schema,
					Host:    host,
					Path:    r.In.URL.Path,
					RawPath: r.In.URL.RawPath,
				}
				q := r.In.URL.Query()
				q.Del(constant.ProxyHostPrefix)
				q.Del(constant.ProxySchemaPrefix)
				q.Del(constant.ProxyRefererPrefix)
				pageOriginURL.RawQuery = q.Encode()
				pageOrigin = pageOriginURL.String()
			}

			// 重写 Origin 头：将代理域名替换为目标站点域名
			// 部分站点校验 Origin，必须使用目标站点的域名
			if origin := r.In.Header.Get("Origin"); origin != "" {
				if originURL, parseErr := url.Parse(origin); parseErr == nil {
					originURL.Scheme = schema
					originURL.Host = host
					r.Out.Header.Set("Origin", originURL.String())
				}
			}

			// 删除 Accept-Encoding，让上游返回未压缩的 HTML，便于改写
			r.Out.Header.Del("Accept-Encoding")

			// 代理入口自身的 scheme
			entryScheme := "http"
			if r.In.TLS != nil {
				entryScheme = "https"
			}

			// 将代理参数存入 context，供 ModifyResponse 使用
			r.Out = r.Out.WithContext(context.WithValue(r.Out.Context(), proxyParamsKey, &proxyParams{
				schema:      schema,
				host:        host,
				proxy:       r.In.Host,
				entryScheme: entryScheme,
				referer:     referer,
				pageOrigin:  pageOrigin,
				currentPath: currentPath,
			}))
		},
		ModifyResponse: func(resp *http.Response) error {
			params, ok := resp.Request.Context().Value(proxyParamsKey).(*proxyParams)
			if !ok {
				return nil
			}

			// 处理重定向 Location
			if isRedirect(resp.StatusCode) {
				rewriteRedirectLocation(resp, params)
				logger.Debug("重定向响应",
					zap.Int("status", resp.StatusCode),
					zap.String("location", resp.Header.Get("Location")),
				)
			}

			// 处理 Set-Cookie：重写 Domain 和 Path
			rewriteCookies(resp, params)

			// 处理 HTML 响应 body 改写
			proxyBase := params.entryScheme + "://" + params.proxy
			if err := rewriter.RewriteResponse(resp, runtimeJS, proxyBase, params.currentPath, params.host, params.schema, params.referer, params.pageOrigin, logger); err != nil {
				logger.Error("改写 HTML 响应失败",
					zap.String("host", params.host),
					zap.String("path", params.currentPath),
					zap.Error(err),
				)
				return err
			}

			return nil
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
}

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
