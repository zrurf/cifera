/**
 * fetch API 拦截器
 * 拦截 window.fetch 调用，改写请求 URL
 */

import { rewriteUrl } from '../rewriter';

const originalFetch = window.fetch;

(window as any).fetch = function(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
    try {
        if (input instanceof Request) {
            // Request 对象：创建新的 Request，改写 URL
            const newUrl = rewriteUrl(input.url);
            if (newUrl !== input.url) {
                input = new Request(newUrl, input);
            }
        } else if (input instanceof URL) {
            input = new URL(rewriteUrl(input.href));
        } else if (typeof input === 'string') {
            input = rewriteUrl(input);
        }
    } catch {
        // 改写失败，保持原样
    }
    return originalFetch.call(this, input, init);
};
