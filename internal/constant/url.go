package constant

// 代理查询参数前缀（_cifera_h/_cifera_s 为协议保留字段）
const ProxyHostPrefix = "_cifera_h"
const ProxySchemaPrefix = "_cifera_s"

// Meta 参数/头前缀（保留：_cifera_h、_cifera_s、Cifera-Referer）
const MetaParamPrefix = "_cifera_"
const MetaHeaderPrefix = "Cifera-"

// Cifera-Referer 头：携带当前页面原始 URL
const HeaderReferer = "Cifera-Referer"

// Cifera-Cookie-Sync 头：客户端 JS 修改的脏 cookie 同步到服务端 Cookie Jar
const HeaderCookieSync = "Cifera-Cookie-Sync"

// Cifera-Cookie-Ack 头：服务端确认客户端 cookie 同步成功
// 格式：name1|path1,name2|path2,...
const HeaderCookieAck = "Cifera-Cookie-Ack"

// Cifera-Cookie-Push 头：服务端增量推送 cookie 变更到客户端
// 格式：base64(JSON array of cookieSyncEntry)，与 Cifera-Cookie-Sync 一致
// 用于非 HTML 响应（如 XHR/fetch），将源站 Set-Cookie 变更实时推送给客户端 Shadow Jar
const HeaderCookiePush = "Cifera-Cookie-Push"

// Cookie Jar 会话 ID cookie 名称
const CookieSessionID = "_cifera_sid"

// 注入到 HTML 中的 JS 全局变量名
const ProxyGlobalVar = "__CIFERA__"
