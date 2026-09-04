/**
 * 导航拦截器：重写所有页面跳转（location 赋值/方法、window.open、<a> 点击、
 * Navigation API、history.pushState/replaceState）
 */

import { rewriteUrl } from '../rewriter';

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

// location 对象无法整体替换，需拦截各方法/属性的 setter
const originalReplace = window.Location.prototype.replace;
const originalAssign = window.Location.prototype.assign;

// location.replace()
window.Location.prototype.replace = function(url: string): void {
    try {
        url = rewriteUrl(url);
    } catch {
        // 改写失败，保持原样
    }
    originalReplace.call(this, url);
};

// location.assign()
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

// location.href setter
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

// window.location 整体赋值无法直接拦截：浏览器中赋值等效于设置 href，已被上方 href setter 覆盖
try {
    const originalLocation = window.location;
    // （遗留说明：此处无法重定义 window.location，保留占位）
} catch {
    // 忽略
}

// <a> 点击拦截：静态 href 已由 Go 端改写，这里处理动态设置的 href
document.addEventListener('click', (e: MouseEvent) => {
    const target = e.target as HTMLElement | null;
    if (!target) return;

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
}, true); // 捕获阶段，确保最先处理

// 拦截 Navigation API 的 navigate 事件（现代浏览器）
if ('navigation' in window && (window as any).navigation) {
    try {
        (window as any).navigation.addEventListener('navigate', (event: any) => {
            try {
                const destinationUrl = event.destination?.url;
                if (destinationUrl) {
                    const rewritten = rewriteUrl(destinationUrl);
                    if (rewritten !== destinationUrl) {
                        event.preventDefault();
                        window.location.href = rewritten;
                    }
                }
            } catch {
                // 改写失败，允许默认行为
            }
        });
    } catch {
        // Navigation API 不可用，跳过
    }
}

// 拦截 history.pushState / replaceState
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

// 导航改写的协议白名单
const ALLOWED_NAV_SCHEMES = new Set([
    'http', 'https', 'ftp', 'ftps',
]);

function shouldSkipNavUrl(url: string): boolean {
    if (!url || url.trim() === '') return true;
    if (url[0] === '#') return true;

    const colonIdx = url.indexOf(':');
    if (colonIdx > 0) {
        const scheme = url.substring(0, colonIdx).toLowerCase();
        // 白名单外协议（jsBridge、weixin 等）不拦截
        if (!ALLOWED_NAV_SCHEMES.has(scheme)) return true;
    }

    return false;
}
