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

// LoadAddons 从指定目录加载所有 addon
// dir: addon 加载目录
// enabled: 仅加载指定 ID 的 addon；若为空则加载全部
func LoadAddons(dir string, enabled []string, logger *zap.Logger) ([]*LoadedAddon, error) {
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

		// 检查 addon.toml 是否存在
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			logger.Debug("跳过无 addon.toml 的目录", zap.String("dir", addonDir))
			continue
		}

		// 解析 addon.toml
		manifest, err := parseManifest(manifestPath)
		if err != nil {
			logger.Error("解析 addon.toml 失败",
				zap.String("path", manifestPath),
				zap.Error(err),
			)
			continue
		}

		// 如果指定了 enabled 列表，检查当前 addon 是否在其中
		if len(enabledSet) > 0 && !enabledSet[manifest.Addon.ID] {
			logger.Debug("addon 未启用，跳过",
				zap.String("id", manifest.Addon.ID),
				zap.String("name", manifest.Addon.Name),
			)
			continue
		}

		// 编译正则并加载资源
		if err := compileRules(manifest, addonDir, logger); err != nil {
			logger.Error("编译 addon 规则失败",
				zap.String("id", manifest.Addon.ID),
				zap.Error(err),
			)
			continue
		}

		// 验证规则合法性
		if err := validateRules(manifest, logger); err != nil {
			logger.Error("addon 规则验证失败",
				zap.String("id", manifest.Addon.ID),
				zap.Error(err),
			)
			continue
		}

		// 验证虚拟主机配置
		if err := validateHosts(manifest, logger); err != nil {
			logger.Error("addon 虚拟主机配置验证失败",
				zap.String("id", manifest.Addon.ID),
				zap.Error(err),
			)
			continue
		}

		addons = append(addons, &LoadedAddon{
			Manifest: *manifest,
			Dir:      addonDir,
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

	// 验证必填字段
	if manifest.Addon.ID == "" {
		return nil, fmt.Errorf("addon.id 不能为空")
	}
	if manifest.Addon.Name == "" {
		return nil, fmt.Errorf("addon.name 不能为空")
	}
	if manifest.Addon.Version == "" {
		return nil, fmt.Errorf("addon.version 不能为空")
	}
	// rules 和 hosts 至少存在一个
	if len(manifest.Rules) == 0 && len(manifest.Hosts) == 0 {
		return nil, fmt.Errorf("至少需要一条 rule 或一个 host 配置")
	}

	return &manifest, nil
}

// compileRules 编译正则表达式并加载资源文件
func compileRules(manifest *AddonManifest, addonDir string, logger *zap.Logger) error {
	for i := range manifest.Rules {
		rule := &manifest.Rules[i]

		// 编译正则表达式
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

		// 验证 action
		switch rule.Action {
		case ActionBlock, ActionReplace, ActionReplaceContent, ActionInject:
			// 合法
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
			rule.resourceContent = content
			rule.resourceType = detectResourceType(rule.Resource)
		}
	}

	return nil
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
			// 验证状态码合法性
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
			// inject 必须指定 position
			if rule.Position == "" {
				return fmt.Errorf("rule[%d]: action=inject 必须指定 position", i)
			}
			// inject 仅支持 JS 和 CSS 资源
			if rule.resourceType != ResourceTypeJS && rule.resourceType != ResourceTypeCSS {
				return fmt.Errorf("rule[%d]: action=inject 仅支持 JS 和 CSS 资源，当前: %s", i, rule.resourceType)
			}
			// 验证 position 值
			switch rule.Position {
			case PositionHeadStart, PositionHeadEnd, PositionBodyStart, PositionBodyEnd:
				// 合法
			default:
				return fmt.Errorf("rule[%d]: 不支持的 position: %s", i, rule.Position)
			}
		}

		// 检查 ID 唯一性在此处不做，因为同一 manifest 内 ID 相同
		// 跨 addon 的 ID 唯一性在 LoadAddons 中已通过 enabled 机制保证
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

// validateHosts 验证虚拟主机配置的基本字段合法性
// 注意：实际的路径解析、目录存在性校验、正则编译在 vhost.LoadFromAddonHosts 中完成
// 此处仅做基本字段校验，避免与 vhost 包逻辑重复
func validateHosts(manifest *AddonManifest, logger *zap.Logger) error {
	for i, hc := range manifest.Hosts {
		// 校验 name 非空且无空白字符
		if hc.Name == "" {
			return fmt.Errorf("hosts[%d]: name 不能为空", i)
		}
		if strings.ContainsAny(hc.Name, " \t\n\r") {
			return fmt.Errorf("hosts[%d]: name 不能包含空白字符: %q", i, hc.Name)
		}

		// 校验 type
		switch hc.Type {
		case vhost.HostTypeLocal, vhost.HostTypeRemote:
			// 合法
		default:
			return fmt.Errorf("hosts[%d]: 无效的 type: %s（支持 local/remote）", i, hc.Type)
		}

		// 校验 priority
		switch hc.Priority {
		case vhost.PriorityOverride, vhost.PriorityFallback:
			// 合法
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
