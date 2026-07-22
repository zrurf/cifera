package vhost

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// 代理参数常量（避免引入 constant 包依赖）
const (
	metaParamPrefix  = "_cifera_"
	metaHeaderPrefix = "Cifera-"
)

// serveRemote 代理到远程 URL
// 使用 http.DefaultTransport.RoundTrip 发起请求，保留原始请求方法和 body
func (h *Host) serveRemote(r *http.Request) (*http.Response, error) {
	targetURL, err := h.buildRemoteURL(r)
	if err != nil {
		return makeErrorResponse(http.StatusBadGateway, "Bad Gateway"), nil
	}

	// 构造新请求（保留原始 method、body、context）
	req, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), r.Body)
	if err != nil {
		return makeErrorResponse(http.StatusBadGateway, "Bad Gateway"), nil
	}

	// 复制请求头
	req.Header = r.Header.Clone()
	// 移除代理特定的 Referer（避免泄漏代理 URL）
	req.Header.Del("Referer")
	// 移除可能的代理转发头
	req.Header.Del("X-Forwarded-Host")
	req.Header.Del("X-Forwarded-Proto")

	// 根据 PassMeta 配置决定是否剔除元信息
	if !h.Config.PassMeta {
		// 移除所有 Cifera-* 头，避免泄漏到源服务器
		for key := range req.Header {
			if strings.HasPrefix(key, metaHeaderPrefix) {
				req.Header.Del(key)
			}
		}
	}

	// 设置 Host 为远程目标的 host
	req.Host = targetURL.Host

	// 发起请求
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return makeErrorResponse(http.StatusBadGateway, "Remote fetch failed"), nil
	}

	return resp, nil
}

// buildRemoteURL 构建远程目标 URL
// 合并 remote base path + request path
// PassMeta=false 时移除所有 _cifera_* 参数；PassMeta=true 时保留
func (h *Host) buildRemoteURL(r *http.Request) (*url.URL, error) {
	// 复制 remote base URL
	target := *h.remoteURL

	// 记录原始 base path（未清理），用于判断末尾是否有 /
	rawBasePath := target.Path
	reqPath := r.URL.Path

	// 合并路径：remote base path + request path
	// 例如 remote="https://cdn.example.com/assets/" + request path="/js/app.js"
	// 结果为 "https://cdn.example.com/assets/js/app.js"
	basePath := path.Clean(rawBasePath)
	if basePath == "." {
		basePath = ""
	}

	if basePath == "" || basePath == "/" {
		// base 为空或根，直接使用请求路径
		target.Path = reqPath
	} else {
		// 拼接路径
		target.Path = path.Join(basePath, reqPath)
		// path.Join 会清理末尾的 /，如果原始 base 以 / 结尾且请求路径为根，保留末尾 /
		if strings.HasSuffix(rawBasePath, "/") && (reqPath == "" || reqPath == "/") {
			target.Path = basePath + "/"
		}
	}

	// 复制查询参数；PassMeta=false 时移除所有 _cifera_* 参数
	q := r.URL.Query()
	if !h.Config.PassMeta {
		for key := range q {
			if strings.HasPrefix(key, metaParamPrefix) {
				q.Del(key)
			}
		}
	}
	target.RawQuery = q.Encode()

	// 保留 fragment
	target.Fragment = r.URL.Fragment

	return &target, nil
}
