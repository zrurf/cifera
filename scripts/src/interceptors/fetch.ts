/**
 * fetch API 拦截器
 * 拦截 window.fetch 调用，改写请求 URL，添加 Cifera-Referer 和 Cifera-Cookie-Sync 头
 * 处理响应中的 Cifera-Cookie-Ack 头确认 cookie 同步
 */

import { rewriteUrl, PROXY_HOST, PROXY_SCHEMA } from '../rewriter';

const originalFetch = window.fetch;

/**
 * 构建当前页面的原始 URL（用于 Cifera-Referer 头）
 */
function buildCiferaReferer(): string {
    if (!PROXY_HOST) return '';
    const schema = PROXY_SCHEMA || 'http';
    const path = window.location.pathname + window.location.search;
    return schema + '://' + PROXY_HOST + path;
}

/**
 * 同步获取脏 cookie 同步头
 */
function getCookieSyncHeaderSync(): string {
    const getDirty = (window as any).__cifera_getCookieSync__;
    if (!getDirty) return '';
    return getDirty();
}

/**
 * 处理 cookie 同步 ACK
 */
function processCookieAck(ack: string): void {
    const fn = (window as any).__cifera_processCookieAck__;
    if (fn) fn(ack);
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

        // 添加 Cifera-Referer 头
        const referer = buildCiferaReferer();
        // 添加 Cifera-Cookie-Sync 头（脏 cookie 增量同步）
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

    // 拦截响应，处理 Cifera-Cookie-Ack 头
    return originalFetch.call(this, input, init).then(response => {
        const ack = response.headers.get('Cifera-Cookie-Ack');
        if (ack) {
            processCookieAck(ack);
        }
        return response;
    });
};
