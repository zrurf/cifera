/**
 * Cookie 托管模块 — Shadow Cookie Jar
 * 用 JS 内存对象替代浏览器原生 Cookie 存储，与服务端 Cookie Jar 协同：
 *  - HTML 注入时经 __CIFERA__.c 全量下发，页面导航时自动重建
 *  - JS 修改后标记脏，经 Cifera-Cookie-Sync 头增量同步，服务端以 Cifera-Cookie-Ack 确认
 *  - 未确认的脏 cookie 下次请求重试；删除用墓碑（e = -1），ACK 确认后才清除
 */

import { PROXY_HOST, PROXY_SCHEMA } from './rewriter';

// Cookie 条目（与 Go 端 cookieSyncEntry 格式一致）
export interface CookieEntry {
    n: string;  // name
    v: string;  // value
    p: string;  // path
    e: number;  // expires：unix 秒，0=会话，-1=删除墓碑
    s: boolean; // secure
    h: boolean; // httponly
}

// cookie 唯一标识：name|path
function cookieKey(name: string, path: string): string {
    return name + '|' + (path || '/');
}

function pathMatch(requestPath: string, cookiePath: string): boolean {
    if (requestPath === cookiePath) return true;
    if (requestPath.startsWith(cookiePath)) {
        if (cookiePath.endsWith('/')) return true;
        if (requestPath[cookiePath.length] === '/') return true;
    }
    return false;
}

// Shadow Cookie Jar（key 为 cookieKey）
const shadowJar = new Map<string, CookieEntry>();

// 未获 ACK 确认的脏 cookie key 集合
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

// 获取匹配当前路径的有效 cookie（过滤过期/墓碑/路径不匹配/secure 不满足）
function getMatchingCookies(): CookieEntry[] {
    const now = Date.now() / 1000;
    const requestPath = window.location.pathname;
    const isSecure = (PROXY_SCHEMA || 'http') === 'https';

    const result: CookieEntry[] = [];
    for (const c of shadowJar.values()) {
        if (c.e === -1) continue;
        if (c.e > 0 && c.e < now) continue;
        if (!pathMatch(requestPath, c.p)) continue;
        if (c.s && !isSecure) continue;
        result.push(c);
    }
    return result;
}

// 构建 document.cookie getter 字符串
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

// Hook document.cookie 的 getter/setter
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

            // 过期则标记为墓碑（e=-1）而非立即删除：随脏同步发给服务器，ACK 确认后才清除
            if (entry.e === -1 || (entry.e > 0 && entry.e < Date.now() / 1000)) {
                const existing = shadowJar.get(key);
                if (existing) {
                    // 保留 name/path，覆盖为墓碑
                    shadowJar.set(key, { n: existing.n, v: '', p: existing.p, e: -1, s: false, h: false });
                } else {
                    // 本地不存在也记录墓碑（服务器端可能存有）
                    shadowJar.set(key, { n: entry.n, v: '', p: entry.p, e: -1, s: false, h: false });
                }
                dirtySet.add(key);
                return;
            }

            shadowJar.set(key, entry);
            dirtySet.add(key);
        },
        configurable: true,
        enumerable: descriptor.enumerable,
    });
}

// 过期 cookie 转墓碑并标记脏，以便同步删除到服务器
function cleanExpired(): void {
    const now = Date.now() / 1000;
    for (const [key, c] of shadowJar) {
        if (c.e > 0 && c.e < now) {
            shadowJar.set(key, { n: c.n, v: '', p: c.p, e: -1, s: false, h: false });
            dirtySet.add(key);
        }
    }
}

// 从 __CIFERA__.c 全量初始化 Shadow Jar（HTML 注入时机）
function initServerCookies(): void {
    const config = (window as any).__CIFERA__;
    if (!config || !config.c) return;

    const serverCookies: CookieEntry[] = config.c;

    shadowJar.clear();

    for (const c of serverCookies) {
        if (c.p === '') c.p = '/';
        shadowJar.set(cookieKey(c.n, c.p), c);
    }

    // 全量注入即一次成功同步，无需再同步回去
    dirtySet.clear();
}

/**
 * 同步获取 Cifera-Cookie-Sync 头值：base64(JSON array of CookieEntry，含墓碑)
 * 不清除脏标记，等 ACK 确认；未确认的 cookie 下次请求重试
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
 * 处理 Cifera-Cookie-Ack：按 cookieKey 列表确认，移除脏标记，
 * 已确认的墓碑从 Shadow Jar 中清除；未确认的保留待下次重试
 */
export function processCookieAck(ack: string): void {
    if (!ack) return;

    const confirmedKeys = ack.split(',').map(s => s.trim()).filter(s => s);

    for (const key of confirmedKeys) {
        dirtySet.delete(key);

        // 已确认的墓碑：从 Shadow Jar 清除
        const entry = shadowJar.get(key);
        if (entry && entry.e === -1) {
            shadowJar.delete(key);
        }
    }
}

/**
 * 处理服务端推送的 cookie 变更（Cifera-Cookie-Push 头，格式与 Sync 一致）
 * 服务端拦截源站 Set-Cookie 后推送，使 fetch/XHR 响应即可生效，无需等下次页面加载；
 * 推送值不标记为脏（服务端已知，无需同步回去）
 */
export function processCookiePush(pushValue: string): void {
    if (!pushValue) return;

    try {
        const decoded = atob(pushValue);
        const entries: CookieEntry[] = JSON.parse(decoded);

        for (const entry of entries) {
            if (entry.p === '') entry.p = '/';
            const key = cookieKey(entry.n, entry.p);

            if (entry.e === -1) {
                // 墓碑：从 Shadow Jar 删除，并清除脏标记
                shadowJar.delete(key);
                dirtySet.delete(key);
            } else {
                // 写入非脏；若此前为脏（客户端也改过），以服务端值为准并清除脏标记
                shadowJar.set(key, entry);
                dirtySet.delete(key);
            }
        }
    } catch {
        // 解码失败，忽略
    }
}

// 挂载到 window 供 fetch/XHR 拦截器调用
(window as any).__cifera_getCookieSync__ = getCookieSyncHeaderSync;
(window as any).__cifera_processCookieAck__ = processCookieAck;
(window as any).__cifera_processCookiePush__ = processCookiePush;

// 初始化：全量同步服务端 cookie，并 Hook document.cookie
export function initCookieManager(): void {
    initServerCookies();
    installCookieHook();
}
