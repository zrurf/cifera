/**
 * WebSocket 拦截器
 * 拦截 WebSocket 构造函数，将 ws(s):// URL 改写为代理 URL
 *
 * 浏览器 WebSocket 构造函数只接受 ws:// 或 wss:// 协议，
 * 因此改写后的 URL 仍使用 ws(s):// 协议，但 host 为代理服务器，
 * 并通过 _cifera_h / _cifera_s 参数传递目标服务器信息。
 *
 * 浏览器会向代理服务器发起 HTTP Upgrade 请求（与 ws/wss 协议无关，
 * WebSocket 连接本质就是 HTTP Upgrade），代理后端识别 Upgrade 请求
 * 后与目标服务器建立 WebSocket 连接并双向桥接。
 */

import { PROXY_HOST, PROXY_SCHEMA } from '../rewriter';

const OriginalWebSocket = window.WebSocket;

// 允许改写的协议
const WS_SCHEMES = new Set(['ws', 'wss']);

/**
 * 判断 URL 是否应跳过改写
 */
function shouldSkip(url: string): boolean {
    if (!url || url.trim() === '') return true;
    if (url.includes('_cifera_')) return true;

    const colonIdx = url.indexOf(':');
    if (colonIdx > 0) {
        const scheme = url.substring(0, colonIdx).toLowerCase();
        if (!WS_SCHEMES.has(scheme)) return true;
    }

    return false;
}

/**
 * 将 WebSocket URL 改写为代理 URL
 *
 * 示例（代理地址 127.0.0.1:8080，源站 example.com）：
 *   ws://example.com/chat     → ws://127.0.0.1:8080/chat?_cifera_h=example.com&_cifera_s=ws
 *   wss://example.com:443/ws  → wss://127.0.0.1:8080/ws?_cifera_h=example.com:443&_cifera_s=wss
 *   ws://cdn.example.com/api  → ws://127.0.0.1:8080/api?_cifera_h=cdn.example.com&_cifera_s=ws
 */
function rewriteWebSocketUrl(url: string | URL): string {
    const urlStr = typeof url === 'string' ? url : url.href;
    if (shouldSkip(urlStr)) return urlStr;

    try {
        const parsed = new URL(urlStr, window.location.href);

        // 目标 host 和 scheme
        const targetHost = parsed.host;
        const targetScheme = parsed.protocol.replace(':', ''); // ws 或 wss

        // 判断是否为同源请求
        const isSameOrigin = parsed.host === window.location.host &&
            parsed.protocol === (window.location.protocol === 'https:' ? 'wss:' : 'ws:');

        if (isSameOrigin) {
            // 同源：只需追加代理参数
            parsed.searchParams.set('_cifera_h', PROXY_HOST);
            parsed.searchParams.set('_cifera_s', targetScheme);
            return parsed.toString();
        }

        // 跨域：构建代理 URL
        // 使用当前页面的 host（代理服务器），协议根据代理服务器是否为 HTTPS 决定
        const proxyScheme = window.location.protocol === 'https:' ? 'wss:' : 'ws:';

        const proxyUrl = new URL(proxyScheme + '//' + window.location.host + parsed.pathname + parsed.search);
        proxyUrl.searchParams.set('_cifera_h', targetHost);
        proxyUrl.searchParams.set('_cifera_s', targetScheme);
        proxyUrl.hash = parsed.hash;

        return proxyUrl.toString();
    } catch {
        return urlStr;
    }
}

// 拦截 WebSocket 构造函数
(window as any).WebSocket = function(url: string | URL, protocols?: string | string[]): any {
    try {
        const rewritten = rewriteWebSocketUrl(url);

        // 创建原始 WebSocket 实例
        let ws: WebSocket;
        if (protocols !== undefined) {
            ws = new OriginalWebSocket(rewritten, protocols);
        } else {
            ws = new OriginalWebSocket(rewritten);
        }

        return ws;
    } catch {
        // 改写失败，使用原始 URL
        if (protocols !== undefined) {
            return new OriginalWebSocket(url, protocols);
        }
        return new OriginalWebSocket(url);
    }
} as any;

// 保留原始 WebSocket 的静态属性
(window as any).WebSocket.CONNECTING = OriginalWebSocket.CONNECTING;
(window as any).WebSocket.OPEN = OriginalWebSocket.OPEN;
(window as any).WebSocket.CLOSING = OriginalWebSocket.CLOSING;
(window as any).WebSocket.CLOSED = OriginalWebSocket.CLOSED;

// 设置原型链，使 instanceof 检查正常工作
(window as any).WebSocket.prototype = OriginalWebSocket.prototype;

// 保留原型上的只读常量
Object.defineProperty((window as any).WebSocket.prototype, 'CONNECTING', {
    value: OriginalWebSocket.CONNECTING,
    writable: false,
    enumerable: true,
    configurable: false,
});
Object.defineProperty((window as any).WebSocket.prototype, 'OPEN', {
    value: OriginalWebSocket.OPEN,
    writable: false,
    enumerable: true,
    configurable: false,
});
Object.defineProperty((window as any).WebSocket.prototype, 'CLOSING', {
    value: OriginalWebSocket.CLOSING,
    writable: false,
    enumerable: true,
    configurable: false,
});
Object.defineProperty((window as any).WebSocket.prototype, 'CLOSED', {
    value: OriginalWebSocket.CLOSED,
    writable: false,
    enumerable: true,
    configurable: false,
});
