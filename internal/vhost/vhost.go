// Package vhost 实现虚拟主机资源服务：将主机名映射到本地目录或远程 URL
// 优先级策略：
//   - override：直接由虚拟主机响应，不经过代理
//   - fallback：先走代理，源站状态码命中 fallback_status 正则时由虚拟主机响应
package vhost

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.uber.org/zap"
)

// HostType 虚拟主机类型
type HostType string

const (
	// HostTypeLocal 本地文件服务
	HostTypeLocal HostType = "local"
	// HostTypeRemote 远程代理服务
	HostTypeRemote HostType = "remote"
)

// Priority 虚拟主机优先级策略
type Priority string

const (
	// PriorityOverride 直接覆盖，不经过代理
	PriorityOverride Priority = "override"
	// PriorityFallback 回退模式，源站返回特定状态码时启用
	PriorityFallback Priority = "fallback"
)

// 默认 fallback 状态码正则：4xx 或 5xx
const defaultFallbackStatus = "4..|5.."

// HostConfig 配置文件 / addon.toml 中的虚拟主机定义
type HostConfig struct {
	// Name 虚拟主机名称（即目标 host，不区分大小写）
	Name string `toml:"name" mapstructure:"name"`
	// Type 主机类型：local / remote
	Type HostType `toml:"type" mapstructure:"type"`
	// Path local 类型的本地目录路径（相对于 config 文件或 addon 目录）
	Path string `toml:"path" mapstructure:"path"`
	// Remote remote 类型的远程 URL（完整 URL，如 https://example.com）
	Remote string `toml:"remote" mapstructure:"remote"`
	// Priority 优先级策略：override / fallback
	Priority Priority `toml:"priority" mapstructure:"priority"`
	// FallbackStatus fallback 模式的状态码正则（支持 ! 前缀取反）
	// 为空时默认 "4..|5.."
	FallbackStatus string `toml:"fallback_status" mapstructure:"fallback_status"`
	// PassMeta 是否在转发请求时携带 Cifera 元信息（_cifera_* 参数和 Cifera-* 头）
	// 开启后，remote vhost 请求将保留元信息，可用于 vhost 间的信息传递
	// 默认 false：元信息会被剔除，避免泄漏到源站
	PassMeta bool `toml:"pass_meta" mapstructure:"pass_meta"`
}

// Host 运行时虚拟主机（已编译/校验）
type Host struct {
	Config         HostConfig
	fallbackRegex  *regexp.Regexp // fallback_status 编译后的正则
	fallbackNegate bool           // fallback_status 是否以 ! 开头（取反匹配）
	absBaseDir     string         // local 类型的绝对路径
	remoteURL      *url.URL       // remote 类型的解析后 URL
	addonID        string         // 来源 addon ID（config 加载的为空）
}

// Registry 虚拟主机注册表
type Registry struct {
	hosts  map[string]*Host // key: host name（小写）
	logger *zap.Logger
}

// NewRegistry 创建虚拟主机注册表
func NewRegistry(logger *zap.Logger) *Registry {
	return &Registry{
		hosts:  make(map[string]*Host),
		logger: logger,
	}
}

// Lookup 查找虚拟主机，顺序：hostParam（_cifera_h）→ requestHost（r.Host）
func (r *Registry) Lookup(hostParam, requestHost string) *Host {
	if hostParam != "" {
		if vh, ok := r.hosts[strings.ToLower(hostParam)]; ok {
			return vh
		}
	}
	if requestHost != "" {
		if vh, ok := r.hosts[strings.ToLower(requestHost)]; ok {
			return vh
		}
	}
	return nil
}

// Register 注册虚拟主机（name 不区分大小写）
// 同名重复注册：后注册的覆盖先注册的，并记录 warning
func (r *Registry) Register(h *Host) error {
	if h == nil {
		return fmt.Errorf("不能注册 nil 虚拟主机")
	}
	if h.Config.Name == "" {
		return fmt.Errorf("虚拟主机 name 不能为空")
	}

	key := strings.ToLower(h.Config.Name)
	if existing, ok := r.hosts[key]; ok {
		owner := "config"
		if existing.addonID != "" {
			owner = "addon:" + existing.addonID
		}
		newOwner := "config"
		if h.addonID != "" {
			newOwner = "addon:" + h.addonID
		}
		r.logger.Warn("虚拟主机名称重复，后者覆盖前者",
			zap.String("name", h.Config.Name),
			zap.String("existing_owner", owner),
			zap.String("new_owner", newOwner),
		)
	}

	r.hosts[key] = h
	return nil
}

// LoadFromConfig 从 config 加载虚拟主机
func (r *Registry) LoadFromConfig(hosts []HostConfig) error {
	for i, hc := range hosts {
		h, err := compileHost(hc, "", r.logger)
		if err != nil {
			return fmt.Errorf("config hosts[%d] (%s): %w", i, hc.Name, err)
		}
		if err := r.Register(h); err != nil {
			return fmt.Errorf("config hosts[%d] (%s): %w", i, hc.Name, err)
		}
		r.logger.Info("加载 config 虚拟主机",
			zap.String("name", h.Config.Name),
			zap.String("type", string(h.Config.Type)),
			zap.String("priority", string(h.Config.Priority)),
		)
	}
	return nil
}

// LoadFromAddonHosts 从 addon 加载虚拟主机
// addonDir: addon 目录（用于解析 local path 的相对路径）
// addonID: 来源 addon ID
func (r *Registry) LoadFromAddonHosts(hosts []HostConfig, addonDir, addonID string) error {
	for i, hc := range hosts {
		h, err := compileHost(hc, addonDir, r.logger)
		if err != nil {
			return fmt.Errorf("addon %s hosts[%d] (%s): %w", addonID, i, hc.Name, err)
		}
		h.addonID = addonID
		if err := r.Register(h); err != nil {
			return fmt.Errorf("addon %s hosts[%d] (%s): %w", addonID, i, hc.Name, err)
		}
		r.logger.Info("加载 addon 虚拟主机",
			zap.String("addon_id", addonID),
			zap.String("name", h.Config.Name),
			zap.String("type", string(h.Config.Type)),
			zap.String("priority", string(h.Config.Priority)),
		)
	}
	return nil
}

// compileHost 编译并校验单个 HostConfig，返回运行时 Host
// addonDir: 解析 local path 相对路径的基准目录（为空则基于工作目录）
func compileHost(hc HostConfig, addonDir string, logger *zap.Logger) (*Host, error) {
	if hc.Name == "" {
		return nil, fmt.Errorf("name 不能为空")
	}
	// name 不允许包含空白字符
	if strings.ContainsAny(hc.Name, " \t\n\r") {
		return nil, fmt.Errorf("name 不能包含空白字符: %q", hc.Name)
	}

	switch hc.Type {
	case HostTypeLocal, HostTypeRemote:
	default:
		return nil, fmt.Errorf("无效的 type: %s（支持 local/remote）", hc.Type)
	}

	switch hc.Priority {
	case PriorityOverride, PriorityFallback:
	default:
		return nil, fmt.Errorf("无效的 priority: %s（支持 override/fallback）", hc.Priority)
	}

	h := &Host{Config: hc}

	if hc.Priority == PriorityFallback {
		pattern := hc.FallbackStatus
		if pattern == "" {
			pattern = defaultFallbackStatus
		}

		// 处理 ! 前缀取反
		if strings.HasPrefix(pattern, "!") {
			h.fallbackNegate = true
			pattern = pattern[1:]
		}

		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("fallback_status 正则编译失败: %w", err)
		}
		h.fallbackRegex = re
	}

	switch hc.Type {
	case HostTypeLocal:
		if hc.Path == "" {
			return nil, fmt.Errorf("local 类型必须指定 path")
		}
		baseDir := hc.Path
		if !filepath.IsAbs(baseDir) {
			// 相对路径基于 addonDir 或工作目录解析
			if addonDir != "" {
				baseDir = filepath.Join(addonDir, baseDir)
			} else {
				wd, err := os.Getwd()
				if err != nil {
					return nil, fmt.Errorf("获取工作目录失败: %w", err)
				}
				baseDir = filepath.Join(wd, baseDir)
			}
		}
		absBase, err := filepath.Abs(baseDir)
		if err != nil {
			return nil, fmt.Errorf("解析本地路径失败: %w", err)
		}
		info, err := os.Stat(absBase)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, fmt.Errorf("本地目录不存在: %s", absBase)
			}
			return nil, fmt.Errorf("访问本地目录失败: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("path 不是目录: %s", absBase)
		}
		h.absBaseDir = absBase

	case HostTypeRemote:
		if hc.Remote == "" {
			return nil, fmt.Errorf("remote 类型必须指定 remote URL")
		}
		remoteURL, err := url.Parse(hc.Remote)
		if err != nil {
			return nil, fmt.Errorf("remote URL 解析失败: %w", err)
		}
		if remoteURL.Scheme != "http" && remoteURL.Scheme != "https" {
			return nil, fmt.Errorf("remote URL 仅支持 http/https，当前: %s", remoteURL.Scheme)
		}
		if remoteURL.Host == "" {
			return nil, fmt.Errorf("remote URL 缺少 host")
		}
		h.remoteURL = remoteURL
	}

	return h, nil
}

// FallbackMatches 判断状态码是否命中 fallback 条件
// fallbackNegate 为 true 时，未匹配正则才返回 true
func (h *Host) FallbackMatches(statusCode int) bool {
	if h.fallbackRegex == nil {
		return false
	}
	code := strconv.Itoa(statusCode)
	matched := h.fallbackRegex.MatchString(code)
	if h.fallbackNegate {
		return !matched
	}
	return matched
}

// Serve 分发请求到 serveLocal / serveRemote
func (h *Host) Serve(r *http.Request) (*http.Response, error) {
	switch h.Config.Type {
	case HostTypeLocal:
		return h.serveLocal(r)
	case HostTypeRemote:
		return h.serveRemote(r)
	default:
		return makeErrorResponse(http.StatusInternalServerError, "Unknown host type"), nil
	}
}
