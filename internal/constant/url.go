package constant

// 代理查询参数前缀
const ProxyHostPrefix = "_cifera_h"   // host前缀
const ProxySchemaPrefix = "_cifera_s" // schema前缀

// Meta 参数/头前缀
const MetaParamPrefix = "_cifera_" // 所有 meta 查询参数前缀（保留：_cifera_h, _cifera_s）
const MetaHeaderPrefix = "Cifera-" // 所有 meta 请求头前缀（保留：Cifera-Referer）

// HeaderReferer Cifera-Referer 头：携带当前页面的原始 URL
const HeaderReferer = "Cifera-Referer"

// HeaderCookieSync Cifera-Cookie-Sync 头：客户端 JS 修改的脏 cookie 同步到服务端 Cookie Jar
const HeaderCookieSync = "Cifera-Cookie-Sync"

// HeaderCookieAck Cifera-Cookie-Ack 头：服务端确认客户端 cookie 同步成功
// 格式：name1|path1,name2|path2,...
const HeaderCookieAck = "Cifera-Cookie-Ack"

// HeaderCookiePush Cifera-Cookie-Push 头：服务端增量推送 cookie 变更到客户端
// 格式：base64(JSON array of cookieSyncEntry)，与 Cifera-Cookie-Sync 格式一致
// 用于非 HTML 响应（如 XHR/fetch）中，将源站 Set-Cookie 的变更实时推送给客户端 Shadow Jar
const HeaderCookiePush = "Cifera-Cookie-Push"

// CookieSessionID Cookie Jar 的会话 ID cookie 名称
const CookieSessionID = "_cifera_sid"

// ProxyGlobalVar 注入到 HTML 中的 JS 全局变量名
const ProxyGlobalVar = "__CIFERA__"
