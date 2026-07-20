package addon

import (
	"regexp"

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
	Position InjectPosition `toml:"position"`

	// 以下为运行时字段，不在 toml 中
	compiledPatterns []*regexp.Regexp // 预编译的正则表达式
	resourceContent  []byte           // 预加载的资源文件内容
	resourceType     ResourceType     // 资源文件类型
}

// AddonManifest addon.toml 的完整结构
type AddonManifest struct {
	Addon AddonMeta          `toml:"addon"`
	Rules []Rule             `toml:"rules"`
	Hosts []vhost.HostConfig `toml:"hosts"`
}

// LoadedAddon 已加载的 addon（manifest + 编译后的正则 + 资源内容）
type LoadedAddon struct {
	Manifest AddonManifest
	Dir      string // addon 目录的绝对路径
}

// InjectItem 表示一个待注入的项目
type InjectItem struct {
	Position     InjectPosition
	Content      []byte
	ResourceType ResourceType
	AddonID      string // 来源 addon 的 ID
}

// MatchResult 表示匹配结果
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

// ResourceType 返回资源文件类型
func (r *Rule) GetResourceType() ResourceType {
	return r.resourceType
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
