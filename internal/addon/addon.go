package addon

import (
	"regexp"
	"sync"

	"github.com/zrurf/cifera/internal/vhost"
)

// ActionType 定义 addon 规则的动作类型
type ActionType string

const (
	// ActionBlock 阻止请求，返回指定状态码（默认 404）
	ActionBlock ActionType = "block"
	// ActionReplace 替换整个响应（body + Content-Type）
	ActionReplace ActionType = "replace"
	// ActionReplaceContent 仅替换响应体，保留原始响应头
	ActionReplaceContent ActionType = "replace_content"
	// ActionInject 向 HTML 注入 JS/CSS
	ActionInject ActionType = "inject"
)

// InjectPosition 定义 inject 动作的注入位置
type InjectPosition string

const (
	// PositionHeadStart 在 <head> 标签后注入
	PositionHeadStart InjectPosition = "head_start"
	// PositionHeadEnd 在 </head> 标签前注入
	PositionHeadEnd InjectPosition = "head_end"
	// PositionBodyStart 在 <body> 标签后注入
	PositionBodyStart InjectPosition = "body_start"
	// PositionBodyEnd 在 </body> 标签前注入
	PositionBodyEnd InjectPosition = "body_end"
)

// InjectRelation 定义选择器注入相对命中元素的位置
type InjectRelation string

const (
	// RelationBefore 在命中元素之前插入
	RelationBefore InjectRelation = "before"
	// RelationAfter 在命中元素之后插入
	RelationAfter InjectRelation = "after"
	// RelationPrepend 在命中元素内最前插入
	RelationPrepend InjectRelation = "prepend"
	// RelationAppend 在命中元素内最后插入
	RelationAppend InjectRelation = "append"
)

// InjectScope 定义选择器注入的命中范围
type InjectScope string

const (
	// ScopeFirst 仅首个命中元素（JS 默认）
	ScopeFirst InjectScope = "first"
	// ScopeAll 全部命中元素（CSS 默认）
	ScopeAll InjectScope = "all"
)

// ResourceType 资源文件类型
type ResourceType string

const (
	ResourceTypeJS    ResourceType = "js"
	ResourceTypeCSS   ResourceType = "css"
	ResourceTypeOther ResourceType = "other"
)

// AddonMeta addon.toml 中的 [addon] 段
type AddonMeta struct {
	ID          string `toml:"id"`          // 必填，addon id，唯一标识（建议使用反向域名格式）
	Name        string `toml:"name"`        // 必填，addon 名称
	Description string `toml:"description"` // 可选，描述
	Version     string `toml:"version"`     // 必填，版本号
	Author      string `toml:"author"`      // 可选，作者
}

// Rule addon.toml 中的 [[rules]] 段
type Rule struct {
	// 必填，正则表达式数组，匹配完整原始 URL，之间的关系为或（匹配任意一个即可）
	Patterns []string `toml:"pattern"`
	// 必填，动作类型：block / replace / replace_content / inject
	Action ActionType `toml:"action"`
	// 资源文件路径（相对于 addon 目录）
	// replace/replace_content/inject 必填，block 可缺省
	Resource string `toml:"resource"`
	// block 的自定义状态码，默认 404
	StatusCode int `toml:"status_code"`
	// inject 的注入位置：head_start / head_end / body_start / body_end
	// 与 At 二选一；position 为空时使用 at 做选择器注入
	Position InjectPosition `toml:"position"`
	// At 选择器注入：CSS 选择器（如 "#root"、".ad-banner"）
	// 指定时表明该规则为选择器注入，需配合 Relation/Scope 使用（仅 HTML）
	At string `toml:"at"`
	// Relation 选择器注入相对位置：before / after / prepend / append
	Relation InjectRelation `toml:"relation"`
	// Scope 选择器注入命中范围：first / all（缺省时 JS=first，CSS=all）
	Scope InjectScope `toml:"scope"`

	// 以下为运行时字段，不在 toml 中
	compiledPatterns []*regexp.Regexp // 预编译的正则表达式
	rawContent       []byte           // 资源文件原始内容（模板源，供租户级重渲染）
	resourceContent  []byte           // 已按全局参数渲染的资源内容
	resourceType     ResourceType     // 资源文件类型
	tenantCache      *ruleTenantCache // 租户级重渲染缓存（懒初始化，避免复制 Mutex）
}

// ruleTenantCache 租户级渲染缓存的共享结构（通过指针引用，避免 Rule 按值复制时复制 Mutex）
type ruleTenantCache struct {
	mu          sync.Mutex
	tenantItems map[string][]byte // tenantID → 渲染结果
}

// AddonManifest addon.toml 的完整结构
type AddonManifest struct {
	Addon  AddonMeta          `toml:"addon"`
	Params []ParamDef         `toml:"params"` // 可选，addon 参数声明
	Rules  []Rule             `toml:"rules"`
	Hosts  []vhost.HostConfig `toml:"hosts"`
}

// LoadedAddon 已加载的 addon（manifest + 编译后的正则 + 资源内容 + 解析后的参数）
type LoadedAddon struct {
	Manifest AddonManifest
	Dir      string         // addon 目录的绝对路径
	Params   map[string]any // 按声明解析后的有效参数（全局/租户合并后）

	globalOverrides map[string]any // 来自 config.addons.params 的原始值，供租户级再次合并（运行时字段）
}

// ParamsFor 计算指定租户叠加覆盖下的有效参数。
// tenantOverrides 来自 tenants.<id>.addon_params，为空时返回全局已解析参数。
// 依次合并 default/global/tenant 三层后重新解析。
func (a *LoadedAddon) ParamsFor(tenantOverrides map[string]any) (map[string]any, error) {
	if len(tenantOverrides) == 0 {
		return a.Params, nil
	}
	// 合并全局覆盖与租户覆盖：租户优先
	merged := make(map[string]any, len(a.globalOverrides)+len(tenantOverrides))
	for k, v := range a.globalOverrides {
		merged[k] = v
	}
	for k, v := range tenantOverrides {
		merged[k] = v
	}
	return ResolveParams(a.Manifest.Params, merged)
}

// InjectItem 待注入的项目
type InjectItem struct {
	Position     InjectPosition
	Content      []byte
	ResourceType ResourceType
	AddonID      string // 来源 addon 的 ID
	At           string // 非空时为选择器注入
	Relation     InjectRelation
	Scope        InjectScope
}

// MatchResult 规则匹配结果
type MatchResult struct {
	Rule    *Rule
	AddonID string
}

// HasResource 判断规则是否有资源文件
func (r *Rule) HasResource() bool {
	return r.Resource != "" && len(r.resourceContent) > 0
}

// ResourceContent 返回资源文件内容
func (r *Rule) ResourceContent() []byte {
	return r.resourceContent
}

// GetResourceType 返回资源文件类型
func (r *Rule) GetResourceType() ResourceType {
	return r.resourceType
}

// ResourceContentFor 返回指定租户上下文下的资源内容。
// 若该租户无个性化参数（params 为 nil），返回全局渲染结果 reourceContent；
// 否则基于模板源 rawContent 用租户参数重渲染，并按 (rule, tenant) 惰性缓存。
// 线程安全：多请求可能同时命中同一 addon。
func (r *Rule) ResourceContentFor(tenantID string, params map[string]any) []byte {
	if len(r.rawContent) == 0 || len(params) == 0 || tenantID == "" {
		return r.resourceContent
	}
	if r.tenantCache == nil {
		r.tenantCache = &ruleTenantCache{}
	}

	tc := r.tenantCache
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if cached, ok := tc.tenantItems[tenantID]; ok {
		return cached
	}

	rendered := r.resourceContent
	if out, err := RenderResource(r.rawContent, params); err == nil {
		rendered = out
	}
	if tc.tenantItems == nil {
		tc.tenantItems = make(map[string][]byte)
	}
	tc.tenantItems[tenantID] = rendered
	return rendered
}

// Match 判断 URL 是否匹配该规则的任一正则
func (r *Rule) Match(url string) bool {
	for _, re := range r.compiledPatterns {
		if re.MatchString(url) {
			return true
		}
	}
	return false
}

// ContentType 根据 replace 动作的资源类型返回 Content-Type
func (r *Rule) ContentType() string {
	switch r.resourceType {
	case ResourceTypeJS:
		return "application/javascript; charset=utf-8"
	case ResourceTypeCSS:
		return "text/css; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}
