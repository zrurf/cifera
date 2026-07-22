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
    // 已包含 _cifera_ 前缀参数，跳过
    if (url.includes('_cifera_')) return true;

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
 * 检测 URL host 是否存在以下问题并修复：
 *  1. host 为代理 host 或其子域拼接（如业务代码 'api.' + window.location.host）
 *     → 将代理 host 部分替换为源站 host（PROXY_HOST），保留子域前缀
 *  2. host 为源站 host 或其子域，但被错误拼接了代理端口（如 'cn.bing.com:' + window.location.port）
 *     → 剥离错误的代理端口
 *
 * 示例（代理 host=127.0.0.1:8080, 源站 host=example.com）：
 *   127.0.0.1:8080         → example.com            （代理 host 本身）
 *   127.0.0.1              → example.com            （代理 hostname）
 *   api.127.0.0.1:8080     → api.example.com        （代理 host 子域）
 *   api.127.0.0.1          → api.example.com        （代理 hostname 子域）
 *   example.com:8080       → example.com            （源站 host + 代理端口）
 *   sub.example.com:8080   → sub.example.com        （源站子域 + 代理端口）
 *   other.com              → other.com              （不匹配，原样返回）
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

    // 步骤 1：检测并剥离错误拼接的 proxy port
	// 页面 JS 可能将 window.location.port 拼接到任意 host 上（包括当前源站、其他源站、代理 host）
	if (proxyPort && host.endsWith(':' + proxyPort)) {
		const hostname = host.slice(0, host.length - proxyPort.length - 1);

		// hostname 是源站 hostname 本身 → 返回 PROXY_HOST（保留源站端口）
		if (hostname === originHostname) {
			return PROXY_HOST;
		}
		// hostname 是源站 hostname 的子域 → 返回 hostname（去掉错误端口）
		if (originHostname && hostname.endsWith('.' + originHostname)) {
			return hostname;
		}
		// hostname 是代理 host 相关 → 去掉端口，继续后续代理 host 检测
		if (hostname === proxyHost || hostname === proxyHostname ||
			hostname.endsWith('.' + proxyHost) || hostname.endsWith('.' + proxyHostname)) {
			host = hostname;
		} else {
			// 其他任意 hostname 携带代理端口：
			// 极大概率是页面 JS 将 window.location.port 拼接到外部 host 上
			// （如 kb.chaoxing.com + ':' + window.location.port → kb.chaoxing.com:8080）
			// 剥离端口，与后端 stripProxyPort 逻辑一致
			host = hostname;
		}
	}

    // 步骤 2：代理 host 检测（原有逻辑）
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
