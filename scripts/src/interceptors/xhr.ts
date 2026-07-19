/**
 * XMLHttpRequest 拦截器
 * 拦截 XMLHttpRequest.prototype.open 调用，改写请求 URL
 */

import { rewriteUrl } from '../rewriter';

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
        // XMLHttpRequest.open 的 async 参数在标准中可选，但大多数实现默认为 true
        if (async === undefined) {
            originalOpen.call(this, method, rewrittenUrl, true);
        } else if (username === undefined || username === null) {
            originalOpen.call(this, method, rewrittenUrl, async);
        } else if (password === undefined || password === null) {
            originalOpen.call(this, method, rewrittenUrl, async, username);
        } else {
            originalOpen.call(this, method, rewrittenUrl, async, username, password);
        }
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
