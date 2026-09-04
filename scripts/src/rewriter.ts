/**
 * URL 改写核心函数
 * 将任意 URL 改写为代理 URL
 */

// 从全局变量读取代理参数
const config = (window as any).__CIFERA__;
export const PROXY_HOST: string = config?.h ?? '';
export const PROXY_SCHEMA: string = config?.s ?? 'http';
export const PROXY_REFERER: string = config?.r ?? '';

// 允许代理改写的协议白名单
// 未知协议（如 jsBridge、weixin 等自定义协议）不拦截
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
    if (url.includes('_cifera_')) return true;

    const colonIdx = url.indexOf(':');
    if (colonIdx > 0) {
        const scheme = url.substring(0, colonIdx).toLowerCase();
        if (!ALLOWED_SCHEMES.has(scheme)) return true;
    }

    return false;
}

/**
 * 代理 host 归一化
 *  1. 代理 host 或其子域（如 'api.' + location.host）→ 替换为源站 host（PROXY_HOST），保留子域前缀
 *  2. 任意 host 被错误拼接代理端口（如 'cn.bing.com:' + location.port）→ 剥离端口
 *
 * 示例（代理 127.0.0.1:8080，源站 example.com）：
 *   api.127.0.0.1:8080  → api.example.com
 *   example.com:8080    → example.com
 *   other.com           → other.com（不匹配，原样返回）
 */
function normalizeProxyHost(host: string): string {
    if (!host || !PROXY_HOST) return host;

    const proxyHost = window.location.host;       // 如 "127.0.0.1:8080"
    const proxyHostname = window.location.hostname; // 如 "127.0.0.1"
    const proxyPort = window.location.port;        // 如 "8080"

    // 提取源站 hostname（PROXY_HOST 可能含端口，如 "example.com:443"）
    let originHostname = PROXY_HOST;
    try {
        originHostname = new URL('http://' + PROXY_HOST).hostname;
    } catch {
        // 解析失败，保持原值
    }

    // 剥离错误拼接的代理端口（页面 JS 常将 location.port 拼到任意 host 上）
	if (proxyPort && host.endsWith(':' + proxyPort)) {
		const hostname = host.slice(0, host.length - proxyPort.length - 1);

		// 源站 hostname 本身：返回 PROXY_HOST（保留源站端口）
		if (hostname === originHostname) {
			return PROXY_HOST;
		}
		// 源站子域：去掉错误端口
		if (originHostname && hostname.endsWith('.' + originHostname)) {
			return hostname;
		}
		// 代理 host 相关：去掉端口，继续后续代理 host 检测
		if (hostname === proxyHost || hostname === proxyHostname ||
			hostname.endsWith('.' + proxyHost) || hostname.endsWith('.' + proxyHostname)) {
			host = hostname;
		} else {
			// 任意外部 host 携带代理端口：剥离端口（与后端 stripProxyPort 一致）
			host = hostname;
		}
	}

    // 完全匹配代理 host（含/不含端口）→ 返回源站 host
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

        try {
            parsed = new URL(url, baseUrl || window.location.href);
        } catch {
            // 解析失败，原样返回
            return url;
        }

        // 归一化代理 host 子域拼接（如 'api.' + location.host），避免二次代理
        const adjustedHost = normalizeProxyHost(parsed.host);
        if (adjustedHost !== parsed.host) {
            parsed.host = adjustedHost;
        }

        const isSameOrigin = parsed.host === window.location.host &&
            parsed.protocol === window.location.protocol;

        if (isSameOrigin) {
            // 同源：仅追加代理参数，host/scheme 不变
            parsed.searchParams.set('_cifera_h', PROXY_HOST);
            if (PROXY_SCHEMA && PROXY_SCHEMA !== 'http') {
                parsed.searchParams.set('_cifera_s', PROXY_SCHEMA);
            }
            return parsed.toString();
        }

        // 跨域：提取目标 host/scheme，重写为代理 URL
        const targetHost = parsed.host;
        const targetSchema = parsed.protocol.replace(':', '');

        // 代理 URL = 当前页面 origin + 原路径 + 目标参数
        const proxyUrl = new URL(parsed.pathname + parsed.search, window.location.origin);
        proxyUrl.searchParams.set('_cifera_h', targetHost);
        if (targetSchema && targetSchema !== 'http') {
            proxyUrl.searchParams.set('_cifera_s', targetSchema);
        }
        proxyUrl.hash = parsed.hash;

        return proxyUrl.toString();
    } catch {
        return url;
    }
}

/**
 * 判断 URL 是否为跨域请求
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
