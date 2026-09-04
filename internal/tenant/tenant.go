// Package tenant 定义租户模型与租户解析逻辑。
// 两种租户方式：
//   - 自动租户（Auto）：无显式标识时以会话 ID 作为租户 ID，随到随用，与旧行为一致；
//   - 注册租户（Managed）：客户端向 API vhost 鉴权取得 token 后，以 _cifera_tid/_cifera_tok
//     标识租户，可实现个性化配置与可控隔离。
//
// 租户 id 与 token 一律经 cookie 承载，绝不放进 URL 参数，避免破坏浏览器缓存。
package tenant

import "fmt"

// Mode 租户类型
type Mode string

const (
	ModeAuto    Mode = "auto"    // 自动租户：会话即租户
	ModeManaged Mode = "managed" // 注册租户：携带 token 的受管租户
)

// Cookie 名称（与 JS 端约定）
const (
	CookieSID = "_cifera_sid" // 会话 ID
	CookieTID = "_cifera_tid" // 租户 ID
	CookieTok = "_cifera_tok" // 租户 token
)

// Tenant 一个租户
type Tenant struct {
	ID   string // 租户 ID
	Mode Mode   // 租户类型
}

// FromSession 以会话 ID 构造自动租户
func FromSession(sessionID string) Tenant {
	return Tenant{ID: sessionID, Mode: ModeAuto}
}

// JarKey 构造步骤 jar 在存储中的命名空间键（tenantID:sessionID）。
// 同一会话 ID 在不同租户下拥有独立的 jar，实现租户级隔离。
func (t Tenant) JarKey(sessionID string) string {
	return t.ID + ":" + sessionID
}

// CookieGetter 读取请求中指定名称的 cookie 值（由 server 注入，便于解耦测试）
type CookieGetter func(name string) string

// Resolver 解析请求所属租户。
// tokenStore 非空时用于校验注册租户的 token；为空则一律视为自动租户。
type Resolver struct {
	tokenStore TokenStore // 注册租户 token 校验（可为 nil）
}

// NewResolver 创建租户解析器
func NewResolver(store TokenStore) *Resolver {
	return &Resolver{tokenStore: store}
}

// Resolve 根据请求 cookie 与缺失会话 id 解析租户。
// sessionID 为当前请求已确定（或新生成）的会话 ID。
func (r *Resolver) Resolve(get CookieGetter, sessionID string) (Tenant, error) {
	// 注册租户：存在租户 id 且 token 校验通过（tokenStore 为 nil 时忽略注册租户，退化为自动）
	if r.tokenStore != nil {
		if tid := get(CookieTID); tid != "" {
			tok := get(CookieTok)
			if r.tokenStore.Valid(tid, tok) {
				return Tenant{ID: tid, Mode: ModeManaged}, nil
			}
		}
	}
	return FromSession(sessionID), nil
}

// TokenStore 注册租户 token 的校验与签发（由外部实现）
type TokenStore interface {
	// Valid 校验租户 id 与 token 是否匹配且未过期
	Valid(tenantID, token string) bool
}

// JarKey 由租户 id 与会话 id 构造命名空间键的便捷函数（供服务端组装）
func JarKey(tenantID, sessionID string) string {
	return fmt.Sprintf("%s:%s", tenantID, sessionID)
}