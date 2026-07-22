/**
 * XMLHttpRequest 拦截器
 * 拦截 XMLHttpRequest.prototype.open 调用，改写请求 URL，添加 Cifera-Referer 和 Cifera-Cookie-Sync 头
 * 处理响应中的 Cifera-Cookie-Ack 头确认 cookie 同步
 */

import { rewriteUrl, PROXY_HOST, PROXY_SCHEMA } from '../rewriter';

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

const originalOpen = XMLHttpRequest.prototype.open;

XMLHttpRequest.prototype.open = function(
    method: string,
    url: string | URL,
    async?: boolean,
    username?: string | null,
    password?: string | null,
): void {
    try {
        const rewrittenUrl = typeof url === 'string' ? rewriteUrl(url) : rewriteUrl(url.href);
        // 保留原始参数传递方式
        if (async === undefined) {
            originalOpen.call(this, method, rewrittenUrl, true);
        } else if (username === undefined || username === null) {
            originalOpen.call(this, method, rewrittenUrl, async);
        } else if (password === undefined || password === null) {
            originalOpen.call(this, method, rewrittenUrl, async, username);
        } else {
            originalOpen.call(this, method, rewrittenUrl, async, username, password);
        }

        // 添加 Cifera-Referer 头
        const referer = buildCiferaReferer();
        if (referer) {
            this.setRequestHeader('Cifera-Referer', referer);
        }

        // 添加 Cifera-Cookie-Sync 头（脏 cookie 增量同步）
        const cookieSync = getCookieSyncHeaderSync();
        if (cookieSync) {
            this.setRequestHeader('Cifera-Cookie-Sync', cookieSync);
        }

        // 监听响应，处理 Cifera-Cookie-Ack 头
        this.addEventListener('load', function() {
            try {
                const ack = this.getResponseHeader('Cifera-Cookie-Ack');
                if (ack) {
                    processCookieAck(ack);
                }
            } catch {
                // 忽略
            }
        });
    } catch {
        // 改写失败，使用原始参数
        if (async === undefined) {
            originalOpen.call(this, method, url, true);
        } else if (username === undefined || username === null) {
            originalOpen.call(this, method, url, async);
        } else if (password === undefined || password === null) {
            originalOpen.call(this, method, url, async, username);
        } else {
            originalOpen.call(this, method, url, async, username, password);
        }
    }
};
