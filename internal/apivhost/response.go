package apivhost

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Response 统一响应结构：成功 code=0，失败时 code 指示业务错误码
type Response struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// 业务错误码
const (
	ErrCodeOK         = 0     // 成功
	ErrCodeBadRequest = 40000 // 请求参数错误
	ErrCodeNoSession  = 40100 // 无有效会话（未登录）
	ErrCodeAuthFailed = 40101 // 鉴权失败（租户/密钥/token 无效）
	ErrCodeNotFound   = 40400 // 资源不存在
	ErrCodeInternal   = 50000 // 服务器内部错误
)

// ok 成功响应：HTTP 200 + code=0
func (h *Handler) ok(w http.ResponseWriter, data any) {
	h.write(w, http.StatusOK, Response{Code: ErrCodeOK, Message: "ok", Data: data})
}

// fail 失败响应：code 为业务错误码，status 为对应 HTTP 状态码
func (h *Handler) fail(w http.ResponseWriter, status, code int, msg string) {
	h.write(w, status, Response{Code: code, Message: msg})
}

// write 编码统一响应并写出
func (h *Handler) write(w http.ResponseWriter, status int, resp Response) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}

// decodeJSON 读取并解码请求 JSON 载荷（限制请求体大小）
func decodeJSON(r *http.Request, out any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	return dec.Decode(out)
}

// errNoSession 无有效会话错误标记
var errNoSession = fmt.Errorf("no session")
