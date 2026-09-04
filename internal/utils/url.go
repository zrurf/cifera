package utils

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/zrurf/cifera/internal/constant"
)

func ParseProxyUrl(u *url.URL) (origin string, schema string, host string) {
	uri := *u
	q := uri.Query()
	schema = q.Get(constant.ProxySchemaPrefix)
	host = q.Get(constant.ProxyHostPrefix)
	if schema == "" {
		schema = "http"
	}

	// 从查询参数中移除所有 _cifera_* 参数，避免发送到源服务器
	StripMetaParams(q)
	uri.RawQuery = q.Encode()

	uri.Host = host
	uri.Scheme = schema
	origin = uri.String()
	return
}

// allowedSchemes 允许代理改写的协议白名单
// 只有这些协议的 URL 才会被改写，未知协议（如 jsBridge、weixin 等自定义协议）不拦截
var allowedSchemes = map[string]bool{
	"http":  true,
	"https": true,
	"ftp":   true,
	"ftps":  true,
	"ws":    true,
	"wss":   true,
}

// shouldSkipUrl 判断 URL 是否跳过代理改写
// 白名单模式：仅改写已知协议的 URL，未知协议不拦截
func shouldSkipUrl(u string) bool {
	if u == "" {
		return true
	}
	// 纯片段
	if u[0] == '#' {
		return true
	}

	colonIdx := strings.Index(u, ":")
	if colonIdx > 0 {
		scheme := strings.ToLower(u[:colonIdx])
		if !allowedSchemes[scheme] {
			return true
		}
	}

	return false
}

// BuildProxyUrl 将原始 URL 转换为代理 URL
// proxyBase: 代理入口地址；currentPath: 当前请求路径（解析相对 URL 用）
// originalUrl: 原始 URL（绝对/相对/协议相对）；host/schema: 目标主机与协议
func BuildProxyUrl(proxyBase, originalUrl, currentPath, host, schema string) string {
	if shouldSkipUrl(originalUrl) {
		return originalUrl
	}

	if ContainsCiferaParam(originalUrl) {
		return originalUrl
	}

	origURL, err := url.Parse(originalUrl)
	if err != nil {
		return originalUrl
	}

	var targetHost, targetSchema string

	switch {
	case origURL.Scheme != "":
		// 绝对 URL：http://cdn.example.com/img.png
		targetHost = origURL.Host
		targetSchema = origURL.Scheme
	case len(originalUrl) > 1 && originalUrl[0] == '/' && originalUrl[1] == '/':
		// 协议相对 URL：//cdn.example.com/img.png
		targetHost = origURL.Host
		targetSchema = schema
		if targetSchema == "" {
			targetSchema = "http"
		}
	default:
		// 相对路径：基于当前路径解析为绝对 URL，再追加代理参数
		base, baseErr := url.Parse(proxyBase + currentPath)
		if baseErr != nil {
			return originalUrl
		}
		resolved := base.ResolveReference(origURL)
		q := resolved.Query()
		q.Set(constant.ProxyHostPrefix, host)
		if schema != "" && schema != "http" {
			q.Set(constant.ProxySchemaPrefix, schema)
		}
		resolved.RawQuery = q.Encode()
		return resolved.String()
	}

	proxyURL, err := url.Parse(proxyBase)
	if err != nil {
		return originalUrl
	}

	proxyURL.Path = origURL.Path
	proxyURL.RawPath = origURL.RawPath

	// 合并查询参数：先保留原始参数，再追加代理参数
	q := proxyURL.Query()
	for key, vals := range origURL.Query() {
		for _, v := range vals {
			q.Set(key, v)
		}
	}
	q.Set(constant.ProxyHostPrefix, targetHost)
	if targetSchema != "" && targetSchema != "http" {
		q.Set(constant.ProxySchemaPrefix, targetSchema)
	}
	proxyURL.RawQuery = q.Encode()
	proxyURL.Fragment = origURL.Fragment

	return proxyURL.String()
}

// ContainsCiferaParam 检查 URL 是否已包含代理参数
func ContainsCiferaParam(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}
	for key := range parsed.Query() {
		if strings.HasPrefix(key, constant.MetaParamPrefix) {
			return true
		}
	}
	return false
}

// StripMetaParams 移除所有 _cifera_ 前缀的查询参数
func StripMetaParams(q url.Values) {
	for key := range q {
		if strings.HasPrefix(key, constant.MetaParamPrefix) {
			q.Del(key)
		}
	}
}

// StripMetaHeaders 移除所有 Cifera- 前缀的请求头
func StripMetaHeaders(h http.Header) {
	for key := range h {
		if strings.HasPrefix(key, constant.MetaHeaderPrefix) {
			h.Del(key)
		}
	}
}
