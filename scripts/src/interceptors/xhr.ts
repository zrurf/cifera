/**
 * XHR 拦截器：改写 open() 的 URL，附加 Cifera-Referer / Cifera-Cookie-Sync 头，
 * 并在响应中处理 Cifera-Cookie-Ack / Cifera-Cookie-Push
 */

import { rewriteUrl, PROXY_HOST, PROXY_SCHEMA } from '../rewriter';

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
        // 按原参数个数透传，保持调用形态一致
        if (async === undefined) {
            originalOpen.call(this, method, rewrittenUrl, true);
        } else if (username === undefined || username === null) {
            originalOpen.call(this, method, rewrittenUrl, async);
        } else if (password === undefined || password === null) {
            originalOpen.call(this, method, rewrittenUrl, async, username);
        } else {
            originalOpen.call(this, method, rewrittenUrl, async, username, password);
        }

        const referer = buildCiferaReferer();
        if (referer) {
            this.setRequestHeader('Cifera-Referer', referer);
        }

        // 脏 cookie 增量同步
        const cookieSync = getCookieSyncHeaderSync();
        if (cookieSync) {
            this.setRequestHeader('Cifera-Cookie-Sync', cookieSync);
        }

        // 拦截响应，处理 Cifera-Cookie-Ack / Cifera-Cookie-Push
        this.addEventListener('load', function() {
            try {
                const ack = this.getResponseHeader('Cifera-Cookie-Ack');
                if (ack) {
                    processCookieAck(ack);
                }
                const push = this.getResponseHeader('Cifera-Cookie-Push');
                if (push) {
                    processCookiePush(push);
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
