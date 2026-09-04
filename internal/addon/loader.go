package addon

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/zrurf/cifera/internal/vhost"
	"go.uber.org/zap"
)

// LoadAddons 从指定目录加载 addon
// enabled 为空时加载全部，否则仅加载其中指定的 addon ID
// globalParams：按 addon ID 分组的关键参数值（来自 config 的 addons.params）
func LoadAddons(dir string, enabled []string, globalParams map[string]map[string]any, logger *zap.Logger) ([]*LoadedAddon, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("解析 addon 目录路径失败: %w", err)
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("addon 目录不存在，跳过加载", zap.String("dir", absDir))
			return nil, nil
		}
		return nil, fmt.Errorf("读取 addon 目录失败: %w", err)
	}

	enabledSet := make(map[string]bool)
	for _, id := range enabled {
		enabledSet[id] = true
	}

	var addons []*LoadedAddon

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		addonDir := filepath.Join(absDir, entry.Name())
		manifestPath := filepath.Join(addonDir, "addon.toml")

		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			logger.Debug("跳过无 addon.toml 的目录", zap.String("dir", addonDir))
			continue
		}

		// 解析 addon.toml（含必填字段校验）
		manifest, err := parseManifest(manifestPath)
		if err != nil {
			logger.Error("解析 addon.toml 失败",
				zap.String("path", manifestPath),
				zap.Error(err),
			)
			continue
		}

		// 仅加载 enabled 列表中出现的 addon
		if len(enabledSet) > 0 && !enabledSet[manifest.Addon.ID] {
			logger.Debug("addon 未启用，跳过",
				zap.String("id", manifest.Addon.ID),
				zap.String("name", manifest.Addon.Name),
			)
			continue
		}

		// 解析 addon 参数（全局关键参数 + addon 声明）
		params, err := ResolveParams(manifest.Params, globalParams[manifest.Addon.ID])
		if err != nil {
			logger.Error("解析 addon 参数失败",
				zap.String("id", manifest.Addon.ID),
				zap.Error(err),
			)
			continue
		}

		// 编译正则并加载资源
		if err := compileRules(manifest, addonDir, params, logger); err != nil {
			logger.Error("编译 addon 规则失败",
				zap.String("id", manifest.Addon.ID),
				zap.Error(err),
			)
			continue
		}

		if err := validateRules(manifest, logger); err != nil {
			logger.Error("addon 规则验证失败",
				zap.String("id", manifest.Addon.ID),
				zap.Error(err),
			)
			continue
		}

		if err := validateHosts(manifest, logger); err != nil {
			logger.Error("addon 虚拟主机配置验证失败",
				zap.String("id", manifest.Addon.ID),
				zap.Error(err),
			)
			continue
		}

		addons = append(addons, &LoadedAddon{
			Manifest:        *manifest,
			Dir:             addonDir,
			Params:          params,
			globalOverrides: globalParams[manifest.Addon.ID],
		})

		ruleCount := len(manifest.Rules)
		hostCount := len(manifest.Hosts)
		logger.Info("addon 加载成功",
			zap.String("id", manifest.Addon.ID),
			zap.String("name", manifest.Addon.Name),
			zap.String("version", manifest.Addon.Version),
			zap.Int("rules", ruleCount),
			zap.Int("hosts", hostCount),
		)
	}

	return addons, nil
}

// parseManifest 解析 addon.toml 文件
func parseManifest(path string) (*AddonManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败: %w", err)
	}

	var manifest AddonManifest
	if err := toml.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("解析 TOML 失败: %w", err)
	}

	if manifest.Addon.ID == "" {
		return nil, fmt.Errorf("addon.id 不能为空")
	}
	if manifest.Addon.Name == "" {
		return nil, fmt.Errorf("addon.name 不能为空")
	}
	if manifest.Addon.Version == "" {
		return nil, fmt.Errorf("addon.version 不能为空")
	}
	// rules 与 hosts 至少存在一个
	if len(manifest.Rules) == 0 && len(manifest.Hosts) == 0 {
		return nil, fmt.Errorf("至少需要一条 rule 或一个 host 配置")
	}
	// 校验参数声明
	if err := validateParams(manifest.Params); err != nil {
		return nil, err
	}

	return &manifest, nil
}

// validateParams 校验参数声明的基本合法性
func validateParams(params []ParamDef) error {
	seen := make(map[string]bool)
	for i, p := range params {
		if p.Name == "" {
			return fmt.Errorf("params[%d]: name 不能为空", i)
		}
		if seen[p.Name] {
			return fmt.Errorf("params[%d]: 参数名重复: %s", i, p.Name)
		}
		seen[p.Name] = true
		if _, err := zeroByType(p.Type); err != nil {
			return fmt.Errorf("params[%d] (%s): %w", i, p.Name, err)
		}
	}
	return nil
}

// compileRules 编译正则表达式，加载资源文件并按参数渲染模板
func compileRules(manifest *AddonManifest, addonDir string, params map[string]any, logger *zap.Logger) error {
	for i := range manifest.Rules {
		rule := &manifest.Rules[i]

		if len(rule.Patterns) == 0 {
			return fmt.Errorf("rule[%d]: pattern 不能为空", i)
		}

		rule.compiledPatterns = make([]*regexp.Regexp, 0, len(rule.Patterns))
		for j, pattern := range rule.Patterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return fmt.Errorf("rule[%d].pattern[%d]: 正则编译失败: %w", i, j, err)
			}
			rule.compiledPatterns = append(rule.compiledPatterns, re)
		}

		switch rule.Action {
		case ActionBlock, ActionReplace, ActionReplaceContent, ActionInject:
		default:
			return fmt.Errorf("rule[%d]: 不支持的 action 类型: %s", i, rule.Action)
		}

		// 加载资源文件（block 可缺省）
		if rule.Resource != "" {
			resourcePath := filepath.Join(addonDir, rule.Resource)
			content, err := os.ReadFile(resourcePath)
			if err != nil {
				return fmt.Errorf("rule[%d]: 加载资源文件失败 (%s): %w", i, resourcePath, err)
			}
			renderResourceContent(rule, content, params, logger)
			rule.rawContent = content
			rule.resourceType = detectResourceType(rule.Resource)
			// 预分配租户渲染缓存，避免按值复制 Rule 后并发写指针（详见 ResourceContentFor）
			rule.tenantCache = &ruleTenantCache{}
		}
	}

	return nil
}

// renderResourceContent 按参数渲染资源模板；模板渲染失败时保留原始内容（容错，避免误伤含字面 {{ 的资源）
func renderResourceContent(rule *Rule, content []byte, params map[string]any, logger *zap.Logger) {
	rendered, err := RenderResource(content, params)
	if err != nil {
		logger.Debug("addon 资源模板渲染失败，使用原始内容",
			zap.String("resource", rule.Resource),
			zap.Error(err),
		)
		rule.resourceContent = content
		return
	}
	rule.resourceContent = rendered
}

// validateRules 验证规则合法性
func validateRules(manifest *AddonManifest, logger *zap.Logger) error {
	for i, rule := range manifest.Rules {
		switch rule.Action {
		case ActionBlock:
			// block 可缺省资源文件，使用默认状态码
			if rule.StatusCode == 0 {
				manifest.Rules[i].StatusCode = 404
			}
			if rule.StatusCode < 100 || rule.StatusCode > 599 {
				return fmt.Errorf("rule[%d]: 无效的 status_code: %d", i, rule.StatusCode)
			}

		case ActionReplace, ActionReplaceContent:
			// replace/replace_content 必须有资源文件
			if !rule.HasResource() {
				return fmt.Errorf("rule[%d]: action=%s 必须指定 resource", i, rule.Action)
			}

		case ActionInject:
			// inject 必须有资源文件
			if !rule.HasResource() {
				return fmt.Errorf("rule[%d]: action=inject 必须指定 resource", i)
			}
			// inject 仅支持 JS 和 CSS 资源
			if rule.resourceType != ResourceTypeJS && rule.resourceType != ResourceTypeCSS {
				return fmt.Errorf("rule[%d]: action=inject 仅支持 JS 和 CSS 资源，当前: %s", i, rule.resourceType)
			}

			// position 与 at 二选一，不可同时缺失或同时指定
			if (rule.Position == "") == (rule.At == "") {
				return fmt.Errorf("rule[%d]: action=inject 必须且只能指定 position 或 at 之一", i)
			}

			if rule.At != "" {
				// 选择器注入：校验 relation 与 scope，并补充默认值
				switch rule.Relation {
				case RelationBefore, RelationAfter, RelationPrepend, RelationAppend:
				default:
					return fmt.Errorf("rule[%d]: 选择器注入无效的 relation: %s", i, rule.Relation)
				}
				if rule.Scope == "" {
					// JS 默认仅首个命中，CSS 默认全部命中（实例级筛选）
					if rule.resourceType == ResourceTypeCSS {
						manifest.Rules[i].Scope = ScopeAll
					} else {
						manifest.Rules[i].Scope = ScopeFirst
					}
				} else if rule.Scope != ScopeFirst && rule.Scope != ScopeAll {
					return fmt.Errorf("rule[%d]: 选择器注入无效的 scope: %s", i, rule.Scope)
				}
			} else {
				switch rule.Position {
				case PositionHeadStart, PositionHeadEnd, PositionBodyStart, PositionBodyEnd:
				default:
					return fmt.Errorf("rule[%d]: 不支持的 position: %s", i, rule.Position)
				}
			}
		}

		// ID 唯一性不在本处检查：同一 manifest 内 ID 唯一，跨 addon 由 LoadAddons 的 enabled 机制保证
		_ = i
	}

	return nil
}

// detectResourceType 根据文件扩展名判断资源类型
func detectResourceType(filename string) ResourceType {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".js", ".mjs":
		return ResourceTypeJS
	case ".css":
		return ResourceTypeCSS
	default:
		return ResourceTypeOther
	}
}

// validateHosts 校验虚拟主机配置的基本字段
// 路径解析、目录校验与正则编译由 vhost.LoadFromAddonHosts 负责，此处不重复
func validateHosts(manifest *AddonManifest, logger *zap.Logger) error {
	for i, hc := range manifest.Hosts {
		// 校验 name 非空且无空白字符
		if hc.Name == "" {
			return fmt.Errorf("hosts[%d]: name 不能为空", i)
		}
		if strings.ContainsAny(hc.Name, " \t\n\r") {
			return fmt.Errorf("hosts[%d]: name 不能包含空白字符: %q", i, hc.Name)
		}

		switch hc.Type {
		case vhost.HostTypeLocal, vhost.HostTypeRemote:
		default:
			return fmt.Errorf("hosts[%d]: 无效的 type: %s（支持 local/remote）", i, hc.Type)
		}

		switch hc.Priority {
		case vhost.PriorityOverride, vhost.PriorityFallback:
		default:
			return fmt.Errorf("hosts[%d]: 无效的 priority: %s（支持 override/fallback）", i, hc.Priority)
		}

		// local 类型必须指定 path
		if hc.Type == vhost.HostTypeLocal && hc.Path == "" {
			return fmt.Errorf("hosts[%d]: local 类型必须指定 path", i)
		}

		// remote 类型必须指定 remote URL 且为合法的 http/https URL
		if hc.Type == vhost.HostTypeRemote {
			if hc.Remote == "" {
				return fmt.Errorf("hosts[%d]: remote 类型必须指定 remote URL", i)
			}
			remoteURL, err := url.Parse(hc.Remote)
			if err != nil {
				return fmt.Errorf("hosts[%d]: remote URL 解析失败: %w", i, err)
			}
			if remoteURL.Scheme != "http" && remoteURL.Scheme != "https" {
				return fmt.Errorf("hosts[%d]: remote URL 仅支持 http/https，当前: %s", i, remoteURL.Scheme)
			}
			if remoteURL.Host == "" {
				return fmt.Errorf("hosts[%d]: remote URL 缺少 host", i)
			}
		}

		// fallback 类型：校验 fallback_status 正则可编译
		if hc.Priority == vhost.PriorityFallback && hc.FallbackStatus != "" {
			pattern := hc.FallbackStatus
			// 处理 ! 前缀取反
			pattern = strings.TrimPrefix(pattern, "!")
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("hosts[%d]: fallback_status 正则编译失败: %w", i, err)
			}
		}
	}

	return nil
}
