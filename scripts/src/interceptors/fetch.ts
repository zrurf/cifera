/**
 * fetch 拦截器：改写请求 URL，附加 Cifera-Referer / Cifera-Cookie-Sync 头，
 * 并在响应中处理 Cifera-Cookie-Ack / Cifera-Cookie-Push
 */

import { rewriteUrl, PROXY_HOST, PROXY_SCHEMA } from '../rewriter';

const originalFetch = window.fetch;

// 源站页面 URL（scheme://PROXY_HOST + 当前路径），作为 Cifera-Referer
function buildCiferaReferer(): string {
    if (!PROXY_HOST) return '';
    const schema = PROXY_SCHEMA || 'http';
    const path = window.location.pathname + window.location.search;
    return schema + '://' + PROXY_HOST + path;
}

// 读取全局挂载的脏 cookie 同步函数
function getCookieSyncHeaderSync(): string {
    const getDirty = (window as any).__cifera_getCookieSync__;
    if (!getDirty) return '';
    return getDirty();
}

// 转发 ACK 给全局处理器
function processCookieAck(ack: string): void {
    const fn = (window as any).__cifera_processCookieAck__;
    if (fn) fn(ack);
}

// 应用服务端推送的 cookie 变更
function processCookiePush(push: string): void {
    const fn = (window as any).__cifera_processCookiePush__;
    if (fn) fn(push);
}

(window as any).fetch = function(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    try {
        let newUrl: string | undefined;
        if (input instanceof Request) {
            newUrl = rewriteUrl(input.url);
            if (newUrl !== input.url) {
                input = new Request(newUrl, input);
            }
        } else if (input instanceof URL) {
            const rewritten = rewriteUrl(input.href);
            if (rewritten !== input.href) {
                input = new URL(rewritten);
            }
        } else if (typeof input === 'string') {
            const rewritten = rewriteUrl(input);
            if (rewritten !== input) {
                input = rewritten;
            }
        }

        const referer = buildCiferaReferer();
        // 脏 cookie 增量同步
        const cookieSync = getCookieSyncHeaderSync();

        if (referer || cookieSync) {
            if (!init) {
                init = {};
            }
            if (!init.headers) {
                init.headers = {};
            }
            if (init.headers instanceof Headers) {
                if (referer) init.headers.set('Cifera-Referer', referer);
                if (cookieSync) init.headers.set('Cifera-Cookie-Sync', cookieSync);
            } else if (Array.isArray(init.headers)) {
                if (referer) init.headers.push(['Cifera-Referer', referer]);
                if (cookieSync) init.headers.push(['Cifera-Cookie-Sync', cookieSync]);
            } else {
                if (referer) (init.headers as Record<string, string>)['Cifera-Referer'] = referer;
                if (cookieSync) (init.headers as Record<string, string>)['Cifera-Cookie-Sync'] = cookieSync;
            }
        }
    } catch {
        // 改写失败，保持原样
    }

    // 拦截响应，处理 Cifera-Cookie-Ack / Cifera-Cookie-Push
    return originalFetch.call(this, input, init).then(response => {
        const ack = response.headers.get('Cifera-Cookie-Ack');
        if (ack) {
            processCookieAck(ack);
        }
        const push = response.headers.get('Cifera-Cookie-Push');
        if (push) {
            processCookiePush(push);
        }
        return response;
    });
};
