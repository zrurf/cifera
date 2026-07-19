/**
 * 导航拦截器
 * 拦截所有页面跳转操作，将 URL 重写为代理 URL
 * 包括：location 赋值、location.href/replace/assign、window.open、<a> 点击等
 */

import { rewriteUrl } from '../rewriter';

// window.open
const originalWindowOpen = window.open;

window.open = function(url?: string | URL, target?: string, features?: string): Window | null {
    try {
        if (url) {
            const rewritten = typeof url === 'string' ? rewriteUrl(url) : rewriteUrl(url.href);
            url = typeof url === 'string' ? rewritten : new URL(rewritten);
        }
    } catch {
        // 改写失败，保持原样
    }
    return originalWindowOpen.call(this, url as any, target, features);
};

// location 赋值拦截
// location 对象是特殊的，无法直接替换，需要通过拦截属性 setter 来实现

// 保存原始 location 方法
const originalReplace = window.Location.prototype.replace;
const originalAssign = window.Location.prototype.assign;

// 拦截 location.replace()
window.Location.prototype.replace = function(url: string): void {
    try {
        url = rewriteUrl(url);
    } catch {
        // 改写失败，保持原样
    }
    originalReplace.call(this, url);
};

// 拦截 location.assign()
if (originalAssign) {
    window.Location.prototype.assign = function(url: string): void {
        try {
            url = rewriteUrl(url);
        } catch {
            // 改写失败，保持原样
        }
        originalAssign.call(this, url);
    };
}

// 拦截 location.href 的 setter
const hrefDescriptor = Object.getOwnPropertyDescriptor(window.Location.prototype, 'href');
if (hrefDescriptor && hrefDescriptor.set) {
    const originalHrefSetter = hrefDescriptor.set;
    Object.defineProperty(window.Location.prototype, 'href', {
        set(this: Location, value: string) {
            try {
                value = rewriteUrl(value);
            } catch {
                // 改写失败，保持原样
            }
            originalHrefSetter.call(this, value);
        },
        get: hrefDescriptor.get,
        enumerable: true,
        configurable: true,
    });
}

// 拦截对 window.location 整体赋值（如 window.location = "http://..."）
// 这通过在 window 上定义 location 的 setter 来实现
// 注意：浏览器中 window.location 是特殊对象，直接赋值等效于设置 href
// 大多数场景已被 href setter 覆盖，这里处理 window.location = url 的写法
try {
    const originalLocation = window.location;
    // 无法直接重定义 window.location，但赋值 window.location = url 等效于 location.href = url
    // 已被 href setter 覆盖
} catch {
    // 忽略
}

// <a> 标签点击拦截
// 拦截所有 <a> 标签的点击事件，改写 href
// 静态 href 已由 Go 端改写，这里处理动态设置的 href
document.addEventListener('click', (e: MouseEvent) => {
    const target = e.target as HTMLElement | null;
    if (!target) return;

    // 查找最近的 <a> 祖先
    const anchor = target.closest('a');
    if (!anchor) return;

    const href = anchor.getAttribute('href');
    if (href && !shouldSkipNavUrl(href)) {
        try {
            const rewritten = rewriteUrl(href);
            if (rewritten !== href) {
                // 阻止默认跳转，手动导航到改写后的 URL
                e.preventDefault();
                window.location.href = rewritten;
            }
        } catch {
            // 改写失败，允许默认行为
        }
    }
}, true); // 使用捕获阶段，确保最先处理

// Navigation API 拦截
// 拦截 Navigation API（现代浏览器支持的 navigate 事件）
if ('navigation' in window && (window as any).navigation) {
    try {
        (window as any).navigation.addEventListener('navigate', (event: any) => {
            try {
                const destinationUrl = event.destination?.url;
                if (destinationUrl) {
                    const rewritten = rewriteUrl(destinationUrl);
                    if (rewritten !== destinationUrl) {
                        // 阻止原始导航
                        event.preventDefault();
                        // 执行改写后的导航
                        window.location.href = rewritten;
                    }
                }
            } catch {
                // 改写失败，允许默认行为
            }
        });
    } catch {
        // Navigation API 不可用，忽略
    }
}

// history API 拦截
// 拦截 history.pushState 和 history.replaceState
const originalPushState = history.pushState.bind(history);
const originalReplaceState = history.replaceState.bind(history);

history.pushState = function(data: any, unused: string, url?: string | URL | null): void {
    if (url) {
        try {
            const urlStr = typeof url === 'string' ? url : url.href;
            const rewritten = rewriteUrl(urlStr);
            url = rewritten;
        } catch {
            // 改写失败，保持原样
        }
    }
    originalPushState(data, unused, url ?? undefined);
};

history.replaceState = function(data: any, unused: string, url?: string | URL | null): void {
    if (url) {
        try {
            const urlStr = typeof url === 'string' ? url : url.href;
            const rewritten = rewriteUrl(urlStr);
            url = rewritten;
        } catch {
            // 改写失败，保持原样
        }
    }
    originalReplaceState(data, unused, url ?? undefined);
};

// 辅助函数
// 允许导航改写的协议白名单
const ALLOWED_NAV_SCHEMES = new Set([
    'http', 'https', 'ftp', 'ftps',
]);

function shouldSkipNavUrl(url: string): boolean {
    if (!url || url.trim() === '') return true;
    if (url[0] === '#') return true;

    // 检查是否包含协议前缀（形如 "xxx:"）
    const colonIdx = url.indexOf(':');
    if (colonIdx > 0) {
        const scheme = url.substring(0, colonIdx).toLowerCase();
        // 只有白名单中的协议才拦截，未知协议（如 jsBridge、weixin 等）跳过
        if (!ALLOWED_NAV_SCHEMES.has(scheme)) return true;
    }

    return false;
}
