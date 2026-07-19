/**
 * URL 改写核心函数
 * 将任意 URL 改写为代理 URL
 */

// 从全局变量读取代理参数
const config = (window as any).__CIFERA__;
const PROXY_HOST: string = config?.h ?? '';
const PROXY_SCHEMA: string = config?.s ?? 'http';
const PROXY_REFERER: string = config?.r ?? '';
// pageOrigin: 当前页面的原始 URL（去掉 _cifera_* 参数后），用于 _cifera_r
const PAGE_ORIGIN: string = config?.p ?? '';

// 允许代理改写的协议白名单
// 只有这些协议的 URL 才会被改写，未知协议（如 jsBridge、weixin 等自定义协议）不拦截
const ALLOWED_SCHEMES = new Set([
    'http', 'https', 'ftp', 'ftps', 'ws', 'wss',
]);

/**
 * 判断 URL 是否应跳过改写
 * 白名单模式：仅改写已知协议的绝对 URL，未知协议不拦截
 */
function shouldSkip(url: string): boolean {
    if (!url || url.trim() === '') return true;
    if (url[0] === '#') return true;
    // 已包含代理参数，跳过
    if (url.includes('_cifera_h=')) return true;

    // 检查是否包含协议前缀（形如 "xxx:"）
    const colonIdx = url.indexOf(':');
    if (colonIdx > 0) {
        const scheme = url.substring(0, colonIdx).toLowerCase();
        // 只有白名单中的协议才改写，未知协议跳过
        if (!ALLOWED_SCHEMES.has(scheme)) return true;
    }

    return false;
}

/**
 * 将 URL 改写为代理 URL
 * @param url 原始 URL
 * @param baseUrl 可选的基础 URL，用于解析相对路径
 */
export function rewriteUrl(url: string, baseUrl?: string): string {
    if (shouldSkip(url)) return url;

    try {
        let parsed: URL;

        // 尝试解析 URL
        try {
            // 如果是绝对 URL，直接解析
            parsed = new URL(url, baseUrl || window.location.href);
        } catch {
            // 如果解析失败，保持原样
            return url;
        }

        // 判断是否为同源请求（目标 host 与当前页面 host 相同）
        const isSameOrigin = parsed.host === window.location.host &&
            parsed.protocol === window.location.protocol;

        if (isSameOrigin) {
            // 同源请求：只需追加代理参数
            parsed.searchParams.set('_cifera_h', PROXY_HOST);
            if (PROXY_SCHEMA && PROXY_SCHEMA !== 'http') {
                parsed.searchParams.set('_cifera_s', PROXY_SCHEMA);
            }
            // 追加 _cifera_r：当前页面的原始 URL
            if (PAGE_ORIGIN) {
                parsed.searchParams.set('_cifera_r', PAGE_ORIGIN);
            }
            return parsed.toString();
        }

        // 跨域请求：提取目标 host/scheme，重写为代理 URL
        const targetHost = parsed.host;
        const targetSchema = parsed.protocol.replace(':', '');

        // 构建代理 URL：使用当前页面 origin + 原始路径
        const proxyUrl = new URL(parsed.pathname + parsed.search, window.location.origin);
        proxyUrl.searchParams.set('_cifera_h', targetHost);
        if (targetSchema && targetSchema !== 'http') {
            proxyUrl.searchParams.set('_cifera_s', targetSchema);
        }
        // 追加 _cifera_r：当前页面的原始 URL
        if (PAGE_ORIGIN) {
            proxyUrl.searchParams.set('_cifera_r', PAGE_ORIGIN);
        }
        proxyUrl.hash = parsed.hash;

        return proxyUrl.toString();
    } catch {
        return url;
    }
}

/**
 * 判断 URL 是否为跨域请求（需要完整改写）
 */
export function isCrossOrigin(url: string): boolean {
    if (shouldSkip(url)) return false;
    try {
        const parsed = new URL(url, window.location.href);
        return parsed.host !== window.location.host;
    } catch {
        return false;
    }
}
