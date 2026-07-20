package vhost

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// 代理参数常量（避免引入 constant 包依赖）
const (
	paramHostPrefix    = "_cifera_h"
	paramSchemaPrefix  = "_cifera_s"
	paramRefererPrefix = "_cifera_r"
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
// 合并 remote base path + request path，移除 _cifera_* 参数
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

	// 复制查询参数并移除 _cifera_* 参数
	q := r.URL.Query()
	q.Del(paramHostPrefix)
	q.Del(paramSchemaPrefix)
	q.Del(paramRefererPrefix)
	target.RawQuery = q.Encode()

	// 保留 fragment
	target.Fragment = r.URL.Fragment

	return &target, nil
}
