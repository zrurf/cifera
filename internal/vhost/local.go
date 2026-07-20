package vhost

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// serveLocal 从本地目录服务文件
// 仅处理 GET/HEAD 方法，其他返回 405
func (h *Host) serveLocal(r *http.Request) (*http.Response, error) {
	// 方法校验：仅允许 GET/HEAD
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return makeErrorResponse(http.StatusMethodNotAllowed, "Method Not Allowed"), nil
	}

	// 安全路径解析
	cleanPath, ok := sanitizePath(h.absBaseDir, r.URL.Path)
	if !ok {
		return makeErrorResponse(http.StatusForbidden, "Forbidden"), nil
	}

	info, err := os.Stat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return makeErrorResponse(http.StatusNotFound, "Not Found"), nil
		}
		return makeErrorResponse(http.StatusInternalServerError, "Internal Server Error"), nil
	}

	// 目录请求：尝试 index.html，失败则 403（禁止目录列举）
	if info.IsDir() {
		indexPath := filepath.Join(cleanPath, "index.html")
		data, err := os.ReadFile(indexPath)
		if err != nil {
			return makeErrorResponse(http.StatusForbidden, "Directory listing forbidden"), nil
		}
		return makeFileResponse(data, "text/html; charset=utf-8", r.Method), nil
	}

	// 读取文件内容
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			return makeErrorResponse(http.StatusNotFound, "Not Found"), nil
		}
		return makeErrorResponse(http.StatusInternalServerError, "Read error"), nil
	}

	// 确定 Content-Type：优先按扩展名，回退到内容嗅探
	contentType := mime.TypeByExtension(filepath.Ext(cleanPath))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}

	return makeFileResponse(data, contentType, r.Method), nil
}

// sanitizePath 目录穿越防护
// 使用 path 包（URL 路径专用，始终用 / 分隔符，跨平台一致）清理请求路径，
// 再用 filepath.Join 拼接到 baseDir（自动处理 OS 分隔符）。
//
// 安全策略：
//  1. path.Clean("/" + requestPath) 前缀 / 确保按绝对路径处理，消除 ..、. 等相对路径组件
//  2. 去掉前导 / 得到相对路径，校验不以 .. 开头（双重保险）
//  3. filepath.Join 拼接后校验最终路径在 baseDir 内
func sanitizePath(baseDir, requestPath string) (string, bool) {
	// 第一重：用 path 包清理 URL 路径（跨平台一致，始终用 / 分隔符）
	// 前缀 / 确保 .. 相对于根解析，无法逃逸
	cleaned := path.Clean("/" + requestPath)

	// 去掉前导 / 得到相对路径
	rel := strings.TrimPrefix(cleaned, "/")

	// 第二重：相对路径不应以 .. 开头（path.Clean 已解析所有 ..，此为双重保险）
	if strings.HasPrefix(rel, "..") {
		return "", false
	}

	// 第三重：用 filepath.Join 拼接到 baseDir（自动处理 OS 分隔符）
	abs := filepath.Join(baseDir, rel)
	finalCleaned := filepath.Clean(abs)

	// 校验最终路径在 baseDir 内
	cleanBase := filepath.Clean(baseDir)
	if finalCleaned != cleanBase && !strings.HasPrefix(finalCleaned, cleanBase+string(filepath.Separator)) {
		return "", false
	}

	return finalCleaned, true
}

// makeFileResponse 构造文件响应
// method: 用于判断是否需要 body（HEAD 请求无 body）
func makeFileResponse(data []byte, contentType, method string) *http.Response {
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
	}
	resp.Header.Set("Content-Type", contentType)
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(data)))
	resp.ContentLength = int64(len(data))

	if method == http.MethodHead {
		resp.Body = http.NoBody
	} else {
		resp.Body = io.NopCloser(bytes.NewReader(data))
	}

	return resp
}

// makeErrorResponse 构造错误响应
func makeErrorResponse(status int, msg string) *http.Response {
	resp := &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
	}
	resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
	body := []byte(msg + "\n")
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	resp.ContentLength = int64(len(body))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp
}
