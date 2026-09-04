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
	upgrader := gws.NewUpgrader(&clientBridgeHandler{
		targetScheme: targetScheme,
		targetHost:   targetHost,
		targetPath:   targetPath,
		referer:      referer,
		cookieStr:    cookieStr,
		logger:       logger,
	}, &gws.ServerOption{
		HandshakeTimeout:   5 * 1e9, // 5s
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

	// 复用 HTTP Server 时，gws 要求 ReadLoop 必须在新 goroutine 中运行
	go clientConn.ReadLoop()
}

// clientBridgeHandler 处理客户端连接事件
// OnOpen 时连接目标服务器，其余事件转发到目标
type clientBridgeHandler struct {
	targetScheme string // 目标协议 ws/wss
	targetHost   string // 目标地址
	targetPath   string // 目标路径（含查询参数）
	referer      string // 原始页面 URL
	cookieStr    string // 转发给目标的 Cookie
	logger       *zap.Logger
}

func (h *clientBridgeHandler) OnOpen(clientConn *gws.Conn) {
	targetURL := h.targetScheme + "://" + h.targetHost + h.targetPath

	reqHeader := http.Header{}

	// 透传客户端的 Sec-WebSocket-Protocol
	if protocols := clientConn.SubProtocol(); protocols != "" {
		reqHeader.Set("Sec-WebSocket-Protocol", protocols)
	}

	// 设置 Origin（部分 WebSocket 服务器会校验）：以目标地址构造
	if h.targetHost != "" {
		originScheme := "http"
		if h.targetScheme == "wss" {
			originScheme = "https"
		}
		reqHeader.Set("Origin", originScheme+"://"+h.targetHost)
	}

	if h.cookieStr != "" {
		reqHeader.Set("Cookie", h.cookieStr)
	}

	targetConn, _, err := gws.NewClient(&targetBridgeHandler{
		clientConn: clientConn,
		logger:     h.logger,
	}, &gws.ClientOption{
		Addr:               targetURL,
		RequestHeader:      reqHeader,
		HandshakeTimeout:   5 * 1e9, // 5s
		ReadMaxPayloadSize: 16 * 1024 * 1024,
	})

	if err != nil {
		h.logger.Error("连接目标 WebSocket 服务器失败",
			zap.String("target", targetURL),
			zap.Error(err),
		)
		_ = clientConn.WriteClose(1006, []byte("upstream connection failed"))
		return
	}

	// 将目标连接存入客户端 Session，后续事件据此转发
	clientConn.Session().Store("target", targetConn)

	// 目标连接的 ReadLoop 须另起 goroutine，避免阻塞 OnOpen 所在的客户端读循环
	go targetConn.ReadLoop()

	h.logger.Debug("WebSocket 代理桥接建立",
		zap.String("target", targetURL),
	)
}

func (h *clientBridgeHandler) OnClose(clientConn *gws.Conn, err error) {
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
	if target, ok := clientConn.Session().Load("target"); ok {
		targetConn := target.(*gws.Conn)
		_ = targetConn.WritePing(payload)
	}
}

func (h *clientBridgeHandler) OnPong(clientConn *gws.Conn, payload []byte) {
	if target, ok := clientConn.Session().Load("target"); ok {
		targetConn := target.(*gws.Conn)
		_ = targetConn.WritePong(payload)
	}
}

func (h *clientBridgeHandler) OnMessage(clientConn *gws.Conn, message *gws.Message) {
	defer message.Close()

	if target, ok := clientConn.Session().Load("target"); ok {
		targetConn := target.(*gws.Conn)
		_ = targetConn.WriteMessage(message.Opcode, message.Bytes())
	}
}

// targetBridgeHandler 处理目标服务器连接事件，将消息转发回客户端
type targetBridgeHandler struct {
	clientConn *gws.Conn
	logger     *zap.Logger
}

func (h *targetBridgeHandler) OnOpen(socket *gws.Conn) {
}

func (h *targetBridgeHandler) OnClose(socket *gws.Conn, err error) {
	if h.clientConn != nil {
		_ = h.clientConn.WriteClose(1000, nil)
	}

	h.logger.Debug("WebSocket 目标连接关闭",
		zap.Error(err),
	)
}

func (h *targetBridgeHandler) OnPing(socket *gws.Conn, payload []byte) {
	if h.clientConn != nil {
		_ = h.clientConn.WritePing(payload)
	}
}

func (h *targetBridgeHandler) OnPong(socket *gws.Conn, payload []byte) {
	if h.clientConn != nil {
		_ = h.clientConn.WritePong(payload)
	}
}

func (h *targetBridgeHandler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()

	if h.clientConn != nil {
		_ = h.clientConn.WriteMessage(message.Opcode, message.Bytes())
	}
}
