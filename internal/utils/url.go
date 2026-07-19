package utils

import (
	"net/url"
	"strings"

	"github.com/zrurf/cifera/internal/constant"
)

func ParseProxyUrl(u *url.URL) (origin string, schema string, host string, referer string) {
	uri := *u
	q := uri.Query()
	schema = q.Get(constant.ProxySchemaPrefix)
	host = q.Get(constant.ProxyHostPrefix)
	referer = q.Get(constant.ProxyRefererPrefix)
	if schema == "" {
		schema = "http"
	}

	// 从查询参数中移除代理参数，避免发送到源服务器
	q.Del(constant.ProxySchemaPrefix)
	q.Del(constant.ProxyHostPrefix)
	q.Del(constant.ProxyRefererPrefix)
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

// shouldSkipUrl 判断 URL 是否不需要代理改写
// 使用白名单模式：仅改写已知协议的绝对 URL，未知协议不拦截
func shouldSkipUrl(u string) bool {
	if u == "" {
		return true
	}
	// 纯片段
	if u[0] == '#' {
		return true
	}

	// 检查是否包含协议前缀（形如 "xxx:"）
	colonIdx := strings.Index(u, ":")
	if colonIdx > 0 {
		// 提取协议部分
		scheme := strings.ToLower(u[:colonIdx])
		// 只有白名单中的协议才改写，未知协议（如 jsBridge、weixin 等自定义协议）跳过
		if !allowedSchemes[scheme] {
			return true
		}
	}

	return false
}

// BuildProxyUrl 将原始 URL 转换为代理 URL
// proxyBase: 代理入口的基础 URL（如 http://127.0.0.1:8080）
// originalUrl: 需要改写的原始 URL（可以是绝对、相对、协议相对）
// currentPath: 当前请求的路径（用于解析相对 URL）
// host: 目标 host
// schema: 目标 scheme
// referer: 当前页面的原始 URL（用于 _cifera_r，即该资源的来源页）
func BuildProxyUrl(proxyBase, originalUrl, currentPath, host, schema, referer string) string {
	if shouldSkipUrl(originalUrl) {
		return originalUrl
	}

	// 已包含代理参数，跳过
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
		// 相对路径：/img.png, ./img.png, ../img.png
		// 基于当前路径解析为绝对 URL，然后追加代理参数
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
		if referer != "" {
			q.Set(constant.ProxyRefererPrefix, referer)
		}
		resolved.RawQuery = q.Encode()
		return resolved.String()
	}

	// 绝对或协议相对 URL：重写为代理 URL
	proxyURL, err := url.Parse(proxyBase)
	if err != nil {
		return originalUrl
	}

	// 设置路径（不含查询参数）
	proxyURL.Path = origURL.Path
	proxyURL.RawPath = origURL.RawPath

	// 合并查询参数：先原始参数，再代理参数
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
	if referer != "" {
		q.Set(constant.ProxyRefererPrefix, referer)
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
	return parsed.Query().Get(constant.ProxyHostPrefix) != ""
}
