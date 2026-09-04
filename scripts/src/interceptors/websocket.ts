/**
 * WebSocket 拦截器
 * 拦截 WebSocket 构造函数，将 ws(s):// URL 改写为代理 URL
 *
 * 浏览器 WebSocket 构造函数只接受 ws/wss 协议，故改写后仍用 ws(s)://，
 * 通过 _cifera_h / _cifera_s 参数携带目标；连接本质是 HTTP Upgrade，
 * 代理后端识别后与目标建立连接并双向桥接
 */

import { PROXY_HOST, PROXY_SCHEMA } from '../rewriter';

const OriginalWebSocket = window.WebSocket;

// 仅改写 ws/wss 协议
const WS_SCHEMES = new Set(['ws', 'wss']);

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

// 改写为代理 URL：ws://代理host/路径?_cifera_h=目标host&_cifera_s=ws
function rewriteWebSocketUrl(url: string | URL): string {
    const urlStr = typeof url === 'string' ? url : url.href;
    if (shouldSkip(urlStr)) return urlStr;

    try {
        const parsed = new URL(urlStr, window.location.href);

        // 目标 host / scheme
        const targetHost = parsed.host;
        const targetScheme = parsed.protocol.replace(':', ''); // ws 或 wss

        const isSameOrigin = parsed.host === window.location.host &&
            parsed.protocol === (window.location.protocol === 'https:' ? 'wss:' : 'ws:');

        if (isSameOrigin) {
            // 同源：仅追加代理参数
            parsed.searchParams.set('_cifera_h', PROXY_HOST);
            parsed.searchParams.set('_cifera_s', targetScheme);
            return parsed.toString();
        }

        // 跨域：代理 URL 用当前页面 host，协议随页面 HTTPS 取 wss/ws
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

(window as any).WebSocket = function(url: string | URL, protocols?: string | string[]): any {
    try {
        const rewritten = rewriteWebSocketUrl(url);

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

// 复制静态常量，保持与原生一致
(window as any).WebSocket.CONNECTING = OriginalWebSocket.CONNECTING;
(window as any).WebSocket.OPEN = OriginalWebSocket.OPEN;
(window as any).WebSocket.CLOSING = OriginalWebSocket.CLOSING;
(window as any).WebSocket.CLOSED = OriginalWebSocket.CLOSED;

// 保持 instanceof 语义
(window as any).WebSocket.prototype = OriginalWebSocket.prototype;

// 原型上的只读常量（与原生一致）
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
