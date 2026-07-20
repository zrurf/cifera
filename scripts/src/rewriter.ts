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
 * 代理 host 归一化
 * 检测 URL host 是否为代理 host 或其子域拼接（如业务代码 'api.' + window.location.host）
 * 若是，将代理 host 部分替换为源站 host（PROXY_HOST），保留子域前缀
 *
 * 示例（代理 host=127.0.0.1:8080, 源站 host=example.com）：
 *   127.0.0.1:8080     → example.com
 *   127.0.0.1          → example.com
 *   api.127.0.0.1:8080 → api.example.com
 *   api.127.0.0.1      → api.example.com
 *   other.com          → other.com（不匹配，原样返回）
 */
function normalizeProxyHost(host: string): string {
    if (!host || !PROXY_HOST) return host;

    const proxyHost = window.location.host;       // 如 "127.0.0.1:8080"
    const proxyHostname = window.location.hostname; // 如 "127.0.0.1"

    // 完全匹配代理 host（含端口）或代理 hostname（不含端口）
    if (host === proxyHost || host === proxyHostname) {
        return PROXY_HOST;
    }

    // 子域拼接：以 ".<proxyHost>" 结尾（如 "api.127.0.0.1:8080"）
    const dotProxyHost = '.' + proxyHost;
    if (host.endsWith(dotProxyHost)) {
        const prefix = host.slice(0, host.length - dotProxyHost.length);
        return prefix + '.' + PROXY_HOST;
    }

    // 子域拼接：以 ".<proxyHostname>" 结尾（如 "api.127.0.0.1"）
    const dotProxyHostname = '.' + proxyHostname;
    if (host.endsWith(dotProxyHostname)) {
        const prefix = host.slice(0, host.length - dotProxyHostname.length);
        return prefix + '.' + PROXY_HOST;
    }

    return host;
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

        // 修复代理 host 子域拼接：将代理 host 部分替换为源站 host
        // 业务代码可能执行 'api.' + window.location.host 得到 'api.127.0.0.1:8080'
        // 此处将其归一化为 'api.<源站host>'，避免错误代理
        const adjustedHost = normalizeProxyHost(parsed.host);
        if (adjustedHost !== parsed.host) {
            parsed.host = adjustedHost;
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
