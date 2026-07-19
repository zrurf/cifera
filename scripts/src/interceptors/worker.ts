/**
 * Worker 拦截器
 * 拦截 Worker / SharedWorker 构造函数，改写脚本 URL
 */

import { rewriteUrl } from '../rewriter';

// 拦截 Worker
const OriginalWorker = window.Worker;

if (OriginalWorker) {
    (window as any).Worker = function(scriptURL: string | URL, options?: WorkerOptions): Worker {
        try {
            const rewrittenUrl = typeof scriptURL === 'string'
                ? rewriteUrl(scriptURL)
                : new URL(rewriteUrl(scriptURL.href));
            return new OriginalWorker(rewrittenUrl, options);
        } catch {
            return new OriginalWorker(scriptURL, options);
        }
    } as any;

    // 保留原型链
    (window as any).Worker.prototype = OriginalWorker.prototype;
}

// 拦截 SharedWorker
const OriginalSharedWorker = (window as any).SharedWorker;

if (OriginalSharedWorker) {
    (window as any).SharedWorker = function(scriptURL: string | URL, options?: any): SharedWorker {
        try {
            const rewrittenUrl = typeof scriptURL === 'string'
                ? rewriteUrl(scriptURL)
                : new URL(rewriteUrl(scriptURL.href));
            return new OriginalSharedWorker(rewrittenUrl, options);
        } catch {
            return new OriginalSharedWorker(scriptURL, options);
        }
    };

    (window as any).SharedWorker.prototype = OriginalSharedWorker.prototype;
}
