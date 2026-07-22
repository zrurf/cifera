/**
 * Cookie 托管模块 — Shadow Cookie Jar
 * 
 * 使用 JS 内存对象维护 Shadow Cookie Jar，替代浏览器原生 Cookie 存储。
 * 与 Cifera 服务端 Cookie Jar 协同工作：
 *  - HTML 注入时，服务端通过 __CIFERA__.c（JSON 数组）下发全量 cookie 列表
 *  - JS 修改 cookie 后，标记为脏，在下一次 fetch/XHR 请求中通过 Cifera-Cookie-Sync 头增量同步给服务器
 *  - 服务器响应 Cifera-Cookie-Ack 头确认同步成功
 *  - 未确认的脏 cookie 会在下一次请求时重新同步（重试机制）
 *  - 页面导航时，新 HTML 会重新注入全量 cookie，Shadow Jar 自动重建
 * 
 * 删除操作：使用墓碑标记（e = -1），保留在 Shadow Jar 中直到 ACK 确认后再清除。
 */

import { PROXY_HOST, PROXY_SCHEMA } from './rewriter';

// Cookie 条目（与 Go 端 cookieSyncEntry 格式一致）
export interface CookieEntry {
    n: string;  // name
    v: string;  // value
    p: string;  // path
    e: number;  // expires (unix timestamp, 0 = session, -1 = 删除墓碑)
    s: boolean; // secure
    h: boolean; // httponly
}

// cookie 唯一标识：name|path
function cookieKey(name: string, path: string): string {
    return name + '|' + (path || '/');
}

// 路径匹配
function pathMatch(requestPath: string, cookiePath: string): boolean {
    if (requestPath === cookiePath) return true;
    if (requestPath.startsWith(cookiePath)) {
        if (cookiePath.endsWith('/')) return true;
        if (requestPath[cookiePath.length] === '/') return true;
    }
    return false;
}

// Shadow Cookie Jar：以 cookieKey 为键的 Map
const shadowJar = new Map<string, CookieEntry>();

// 脏标记集合：未被 ACK 确认的 cookie key
const dirtySet = new Set<string>();

/**
 * 解析 document.cookie setter 字符串
 */
function parseCookieString(cookieStr: string): CookieEntry | null {
    const parts = cookieStr.split(';');
    if (parts.length === 0) return null;

    const nameValue = parts[0].trim();
    const eqIdx = nameValue.indexOf('=');
    if (eqIdx < 0) return null;

    const name = nameValue.substring(0, eqIdx).trim();
    const value = nameValue.substring(eqIdx + 1).trim();
    if (!name) return null;

    const entry: CookieEntry = {
        n: name,
        v: value,
        p: '/',
        e: 0,
        s: false,
        h: false,
    };

    for (let i = 1; i < parts.length; i++) {
        const part = parts[i].trim();
        const colonEq = part.indexOf('=');
        const attrName = (colonEq >= 0 ? part.substring(0, colonEq) : part).trim().toLowerCase();
        const attrValue = colonEq >= 0 ? part.substring(colonEq + 1).trim() : '';

        switch (attrName) {
            case 'path':
                entry.p = attrValue || '/';
                break;
            case 'secure':
                entry.s = true;
                break;
            case 'httponly':
                entry.h = true;
                break;
            case 'max-age': {
                const seconds = parseInt(attrValue, 10);
                if (!isNaN(seconds)) {
                    if (seconds <= 0) {
                        entry.e = -1; // 删除标记（墓碑）
                    } else {
                        entry.e = Math.floor(Date.now() / 1000) + seconds;
                    }
                }
                break;
            }
            case 'expires': {
                const date = new Date(attrValue);
                if (!isNaN(date.getTime())) {
                    entry.e = Math.floor(date.getTime() / 1000);
                }
                break;
            }
        }
    }

    return entry;
}

/**
 * 从 Shadow Jar 获取匹配当前路径的 cookie 列表（已过滤过期/墓碑/不匹配）
 */
function getMatchingCookies(): CookieEntry[] {
    const now = Date.now() / 1000;
    const requestPath = window.location.pathname;
    const isSecure = (PROXY_SCHEMA || 'http') === 'https';

    const result: CookieEntry[] = [];
    for (const c of shadowJar.values()) {
        // 墓碑不返回
        if (c.e === -1) continue;
        // 过期检查
        if (c.e > 0 && c.e < now) continue;
        // 路径匹配
        if (!pathMatch(requestPath, c.p)) continue;
        // secure 检查
        if (c.s && !isSecure) continue;
        result.push(c);
    }
    return result;
}

/**
 * 从 CookieEntry 列表构建 document.cookie getter 字符串
 */
function buildCookieString(entries: CookieEntry[]): string {
    return entries
        .filter(c => !c.h) // httponly 对 JS 不可见
        .sort((a, b) => {
            if (a.p.length !== b.p.length) return b.p.length - a.p.length;
            return a.n.localeCompare(b.n);
        })
        .map(c => c.n + '=' + c.v)
        .join('; ');
}

/**
 * Hook document.cookie
 */
function installCookieHook(): void {
    const descriptor = Object.getOwnPropertyDescriptor(Document.prototype, 'cookie') ||
        Object.getOwnPropertyDescriptor(HTMLDocument.prototype, 'cookie');

    if (!descriptor) return;

    Object.defineProperty(document, 'cookie', {
        get() {
            cleanExpired();
            return buildCookieString(getMatchingCookies());
        },
        set(val: string) {
            const entry = parseCookieString(val);
            if (!entry) return;

            const key = cookieKey(entry.n, entry.p);

            // max-age <= 0 或已过期：标记为墓碑（e = -1），不立即删除
            // 墓碑会随脏同步发送给服务器，ACK 后才清除
            if (entry.e === -1 || (entry.e > 0 && entry.e < Date.now() / 1000)) {
                const existing = shadowJar.get(key);
                if (existing) {
                    // 设置墓碑：保留 name/path 信息，标记 e = -1
                    shadowJar.set(key, { n: existing.n, v: '', p: existing.p, e: -1, s: false, h: false });
                } else {
                    // cookie 不存在但也记录墓碑（可能服务器端有）
                    shadowJar.set(key, { n: entry.n, v: '', p: entry.p, e: -1, s: false, h: false });
                }
                dirtySet.add(key);
                return;
            }

            // 更新 Shadow Jar
            shadowJar.set(key, entry);
            dirtySet.add(key);
        },
        configurable: true,
        enumerable: descriptor.enumerable,
    });
}

/**
 * 清理过期 cookie（标记为脏以便同步删除到服务器）
 */
function cleanExpired(): void {
    const now = Date.now() / 1000;
    for (const [key, c] of shadowJar) {
        if (c.e > 0 && c.e < now) {
            // 过期 cookie 转为墓碑
            shadowJar.set(key, { n: c.n, v: '', p: c.p, e: -1, s: false, h: false });
            dirtySet.add(key);
        }
    }
}

/**
 * 初始化服务端注入的 cookie（全量同步）
 * 从 __CIFERA__.c 读取 cookie 数组，写入 Shadow Jar
 */
function initServerCookies(): void {
    const config = (window as any).__CIFERA__;
    if (!config || !config.c) return;

    const serverCookies: CookieEntry[] = config.c;

    // 清空 Shadow Jar
    shadowJar.clear();

    // 写入服务端下发的 cookie
    for (const c of serverCookies) {
        if (c.p === '') c.p = '/';
        shadowJar.set(cookieKey(c.n, c.p), c);
    }

    // 全量同步后清除脏标记（全量注入本身是一次成功的同步）
    dirtySet.clear();
}

/**
 * 同步获取脏 cookie 同步头值
 * 从 Shadow Jar 内存构建，可安全在拦截器中同步调用
 * 格式：base64(JSON array of CookieEntry)
 * 包含墓碑条目（e = -1）表示删除操作
 * 
 * 注意：不立即清除脏标记，等待 ACK 确认后才清除
 * 如果 ACK 未收到，下次请求会重新同步
 */
export function getCookieSyncHeaderSync(): string {
    if (dirtySet.size === 0) return '';

    const dirtyCookies: CookieEntry[] = [];
    for (const key of dirtySet) {
        const c = shadowJar.get(key);
        if (c) {
            dirtyCookies.push(c);
        }
    }

    if (dirtyCookies.length === 0) {
        dirtySet.clear();
        return '';
    }

    try {
        const json = JSON.stringify(dirtyCookies);
        return btoa(json);
    } catch {
        return '';
    }
}

/**
 * 处理 ACK 确认
 * 格式：name1|path1,name2|path2,...
 * 每个条目是 cookieKey 格式（与 dirtySet 中的 key 一致）
 * 
 * 确认成功的 cookie 从脏标记中移除，墓碑从 Shadow Jar 中清除
 * 未确认的 cookie 保留在脏标记中，下次请求时重试同步
 */
export function processCookieAck(ack: string): void {
    if (!ack) return;

    const confirmedKeys = ack.split(',').map(s => s.trim()).filter(s => s);

    for (const key of confirmedKeys) {
        dirtySet.delete(key);

        // 如果是墓碑（已删除的 cookie），确认后从 Shadow Jar 中清除
        const entry = shadowJar.get(key);
        if (entry && entry.e === -1) {
            shadowJar.delete(key);
        }
    }
}

// 挂载到 window，供拦截器使用
(window as any).__cifera_getCookieSync__ = getCookieSyncHeaderSync;
(window as any).__cifera_processCookieAck__ = processCookieAck;

/**
 * 初始化 Cookie 托管系统
 * 1. 从 __CIFERA__.c 全量同步服务端 cookie 到 Shadow Jar
 * 2. Hook document.cookie
 */
export function initCookieManager(): void {
    initServerCookies();
    installCookieHook();
}
