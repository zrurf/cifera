package rewriter

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/zrurf/cifera/internal/addon"
	"go.uber.org/zap"
)

// RewriteResponse 检测并改写 HTML 响应
// 如果响应是 text/html 且内容包含实际 HTML 结构，则改写静态 URL 并注入 JS 运行时
// addonInjects: addon 的 inject 列表，运行时 JS 优先注入（<head> 标签后），addon 注入在其之后
func RewriteResponse(resp *http.Response, runtimeJS, proxyBase, currentPath, host, schema, referer, cookiesJSON string, addonInjects []addon.InjectItem, logger *zap.Logger) error {
	// 检测 Content-Type 是否为 HTML
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "text/html") {
		return nil
	}

	// 读取完整 body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Error("读取响应 body 失败",
			zap.String("host", host),
			zap.String("path", currentPath),
			zap.Error(err),
		)
		return err
	}
	resp.Body.Close()

	// 空响应，直接返回
	if len(body) == 0 {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}

	// 检查内容是否包含实际的 HTML 结构
	// 部分网站不规范，Content-Type 为 text/html 但实际返回 JSON 或纯文本
	if !looksLikeHTML(body) {
		logger.Debug("响应 Content-Type 为 text/html 但内容非 HTML，跳过改写",
			zap.String("host", host),
			zap.String("path", currentPath),
			zap.Int("body_len", len(body)),
		)
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}

	// 改写 HTML 中的静态 URL
	rewritten := RewriteHTMLUrls(body, proxyBase, currentPath, host, schema)

	// 先注入 addon 的 JS/CSS，再注入 JS 运行时
	// 这样 InjectRuntime 会将运行时代码插入到 <head> 标签后（最前面），
	// 确保所有 addon 注入的 JS（包括 head_start 位置）都在运行时之后，
	// addon JS 可以使用 cifera runtime 提供的能力和接口
	if len(addonInjects) > 0 {
		rewritten = addon.InjectAddons(rewritten, addonInjects)
	}

	// 注入 JS 运行时（在 <head> 标签后，优先于所有 addon 注入内容）
	rewritten = InjectRuntime(rewritten, runtimeJS, host, schema, referer, cookiesJSON)

	// 设置新 body
	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	// 删除可能存在的压缩相关 header，因为 body 已被改写为未压缩
	resp.Header.Del("Content-Encoding")

	logger.Debug("HTML 响应改写完成",
		zap.String("host", host),
		zap.String("path", currentPath),
		zap.Int("original_len", len(body)),
		zap.Int("rewritten_len", len(rewritten)),
	)

	return nil
}

// looksLikeHTML 检查 body 内容是否看起来像实际的 HTML
// 通过检测常见的 HTML 标签来判断，避免对非 HTML 内容（如 JSON、纯文本）误注入
func looksLikeHTML(body []byte) bool {
	// 只检查前 1024 字节，避免大 body 的性能开销
	checkLen := min(len(body), 1024)
	head := strings.ToLower(string(body[:checkLen]))

	// 检测常见的 HTML 标识
	htmlIndicators := []string{
		"<!doctype",
		"<html",
		"<head",
		"<body",
		"<div",
		"<script",
		"<link ",
		"<meta ",
		"<title",
		"<table",
		"<form",
		"<input",
		"<img ",
		"<span",
		"<p>",
		"<ul",
		"<ol",
	}

	for _, indicator := range htmlIndicators {
		if strings.Contains(head, indicator) {
			return true
		}
	}

	return false
}
