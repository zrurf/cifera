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

// RewriteResponse 改写 HTML 响应中的静态 URL 并注入 JS 运行时
// 仅处理 text/html 且内容含实际 HTML 结构的响应
// 先注入 addon 内容，后注入运行时，保证运行时位于 <head> 后最前、addon JS 可调用其 API
func RewriteResponse(resp *http.Response, runtimeJS, proxyBase, currentPath, host, schema, referer, cookiesJSON string, addonInjects []addon.InjectItem, logger *zap.Logger) error {
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if !strings.Contains(contentType, "text/html") {
		return nil
	}

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

	if len(body) == 0 {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}

	// 部分网站 Content-Type 为 text/html 但实际返回 JSON 或纯文本，需二次确认
	if !looksLikeHTML(body) {
		logger.Debug("响应 Content-Type 为 text/html 但内容非 HTML，跳过改写",
			zap.String("host", host),
			zap.String("path", currentPath),
			zap.Int("body_len", len(body)),
		)
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}

	rewritten := RewriteHTMLUrls(body, proxyBase, currentPath, host, schema)

	// 先注入 addon，再注入运行时：运行时会插到 <head> 后最前，
	// 使所有 addon JS（含 head_start 位置）位于运行时之后，可使用 runtime API
	if len(addonInjects) > 0 {
		rewritten = addon.InjectAddons(rewritten, addonInjects)
	}

	rewritten = InjectRuntime(rewritten, runtimeJS, host, schema, referer, cookiesJSON)

	resp.Body = io.NopCloser(bytes.NewReader(rewritten))
	resp.ContentLength = int64(len(rewritten))
	resp.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	// body 已改写为未压缩内容，需删除压缩相关 header
	resp.Header.Del("Content-Encoding")

	logger.Debug("HTML 响应改写完成",
		zap.String("host", host),
		zap.String("path", currentPath),
		zap.Int("original_len", len(body)),
		zap.Int("rewritten_len", len(rewritten)),
	)

	return nil
}

// looksLikeHTML 通过常见标签判断内容是否为真实 HTML，避免误注入到 JSON/纯文本
func looksLikeHTML(body []byte) bool {
	// 只检查前 1024 字节，避免大 body 的性能开销
	checkLen := min(len(body), 1024)
	head := strings.ToLower(string(body[:checkLen]))

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
