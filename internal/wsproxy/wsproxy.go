// Package wsproxy 实现 WebSocket 反向代理
// 将客户端的 WebSocket Upgrade 请求桥接到目标 WebSocket 服务器
package wsproxy

import (
	"net/http"
	"strings"

	"github.com/lxzan/gws"
	"go.uber.org/zap"
)

// IsWebSocketUpgrade 检测 HTTP 请求是否为 WebSocket Upgrade 请求
func IsWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// ProxyWebSocket 处理 WebSocket 代理请求
// 将客户端 WebSocket 连接桥接到目标 WebSocket 服务器
//
// 参数：
//   - w: HTTP ResponseWriter（用于 Upgrade 客户端连接）
//   - r: HTTP 请求
//   - targetScheme: 目标 WebSocket 协议（ws 或 wss）
//   - targetHost: 目标服务器 host（如 example.com:8080）
//   - targetPath: 目标服务器路径（含查询参数，已移除 _cifera_* 参数）
//   - referer: 原始页面的 URL（用于设置 Origin 头）
//   - cookieStr: 要发送到目标服务器的 Cookie 字符串
//   - logger: 日志记录器
func ProxyWebSocket(w http.ResponseWriter, r *http.Request, targetScheme, targetHost, targetPath, referer, cookieStr string, logger *zap.Logger) {
	// 构建 Upgrader：将客户端 HTTP 连接升级为 WebSocket
	upgrader := gws.NewUpgrader(&clientBridgeHandler{
		targetScheme: targetScheme,
		targetHost:   targetHost,
		targetPath:   targetPath,
		referer:      referer,
		cookieStr:    cookieStr,
		logger:       logger,
	}, &gws.ServerOption{
		HandshakeTimeout:  5 * 1e9, // 5s
		ReadMaxPayloadSize: 16 * 1024 * 1024,
	})

	clientConn, err := upgrader.Upgrade(w, r)
	if err != nil {
		logger.Error("WebSocket Upgrade 失败",
			zap.String("host", targetHost),
			zap.String("path", targetPath),
			zap.Error(err),
		)
		return
	}

	// 在独立 goroutine 中运行 ReadLoop（gws 要求：复用 HTTP Server 时 ReadLoop 必须在新 goroutine 中）
	go clientConn.ReadLoop()
}

// clientBridgeHandler 处理客户端 WebSocket 连接的事件
// OnOpen 时连接目标服务器，OnMessage/OnPing/OnClose 时转发到目标
type clientBridgeHandler struct {
	targetScheme string       // 目标协议 ws/wss
	targetHost   string       // 目标 host
	targetPath   string       // 目标路径（含查询参数）
	referer      string       // 原始页面 URL
	cookieStr    string       // Cookie 字符串
	logger       *zap.Logger  // 日志
}

func (h *clientBridgeHandler) OnOpen(clientConn *gws.Conn) {
	// 构建目标 WebSocket URL
	targetURL := h.targetScheme + "://" + h.targetHost + h.targetPath

	// 构建发往目标服务器的请求头
	reqHeader := http.Header{}

	// 透传客户端的 Sec-WebSocket-Protocol
	if protocols := clientConn.SubProtocol(); protocols != "" {
		reqHeader.Set("Sec-WebSocket-Protocol", protocols)
	}

	// 设置 Origin：部分 WebSocket 服务器校验 Origin
	// 使用目标 scheme + host 构造
	if h.targetHost != "" {
		originScheme := "http"
		if h.targetScheme == "wss" {
			originScheme = "https"
		}
		reqHeader.Set("Origin", originScheme+"://"+h.targetHost)
	}

	// 设置 Cookie
	if h.cookieStr != "" {
		reqHeader.Set("Cookie", h.cookieStr)
	}

	// 连接目标 WebSocket 服务器
	targetConn, _, err := gws.NewClient(&targetBridgeHandler{
		clientConn: clientConn,
		logger:     h.logger,
	}, &gws.ClientOption{
		Addr:          targetURL,
		RequestHeader: reqHeader,
		HandshakeTimeout: 5 * 1e9, // 5s
		ReadMaxPayloadSize: 16 * 1024 * 1024,
	})

	if err != nil {
		h.logger.Error("连接目标 WebSocket 服务器失败",
			zap.String("target", targetURL),
			zap.Error(err),
		)
		// 关闭客户端连接
		_ = clientConn.WriteClose(1006, []byte("upstream connection failed"))
		return
	}

	// 将目标连接存入客户端连接的 Session，以便后续事件转发
	clientConn.Session().Store("target", targetConn)

	// 在独立 goroutine 中运行目标连接的 ReadLoop
	go targetConn.ReadLoop()

	h.logger.Debug("WebSocket 代理桥接建立",
		zap.String("target", targetURL),
	)
}

func (h *clientBridgeHandler) OnClose(clientConn *gws.Conn, err error) {
	// 客户端断开连接，关闭目标连接
	if target, ok := clientConn.Session().Load("target"); ok {
		targetConn := target.(*gws.Conn)
		_ = targetConn.WriteClose(1000, nil)
	}

	h.logger.Debug("WebSocket 客户端连接关闭",
		zap.String("host", h.targetHost),
		zap.String("path", h.targetPath),
		zap.Error(err),
	)
}

func (h *clientBridgeHandler) OnPing(clientConn *gws.Conn, payload []byte) {
	// 转发 Ping 到目标服务器
	if target, ok := clientConn.Session().Load("target"); ok {
		targetConn := target.(*gws.Conn)
		_ = targetConn.WritePing(payload)
	}
}

func (h *clientBridgeHandler) OnPong(clientConn *gws.Conn, payload []byte) {
	// 转发 Pong 到目标服务器
	if target, ok := clientConn.Session().Load("target"); ok {
		targetConn := target.(*gws.Conn)
		_ = targetConn.WritePong(payload)
	}
}

func (h *clientBridgeHandler) OnMessage(clientConn *gws.Conn, message *gws.Message) {
	defer message.Close()

	// 转发客户端消息到目标服务器
	if target, ok := clientConn.Session().Load("target"); ok {
		targetConn := target.(*gws.Conn)
		_ = targetConn.WriteMessage(message.Opcode, message.Bytes())
	}
}

// targetBridgeHandler 处理目标 WebSocket 服务器连接的事件
// 将目标服务器的响应转发回客户端
type targetBridgeHandler struct {
	clientConn *gws.Conn  // 客户端连接
	logger     *zap.Logger
}

func (h *targetBridgeHandler) OnOpen(socket *gws.Conn) {
	// 目标服务器连接已建立，无需额外操作
}

func (h *targetBridgeHandler) OnClose(socket *gws.Conn, err error) {
	// 目标服务器断开连接，关闭客户端连接
	if h.clientConn != nil {
		_ = h.clientConn.WriteClose(1000, nil)
	}

	h.logger.Debug("WebSocket 目标连接关闭",
		zap.Error(err),
	)
}

func (h *targetBridgeHandler) OnPing(socket *gws.Conn, payload []byte) {
	// 转发目标的 Ping 到客户端
	if h.clientConn != nil {
		_ = h.clientConn.WritePing(payload)
	}
}

func (h *targetBridgeHandler) OnPong(socket *gws.Conn, payload []byte) {
	// 转发目标的 Pong 到客户端
	if h.clientConn != nil {
		_ = h.clientConn.WritePong(payload)
	}
}

func (h *targetBridgeHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()

	// 转发目标服务器消息到客户端
	if h.clientConn != nil {
		_ = h.clientConn.WriteMessage(message.Opcode, message.Bytes())
	}
}
