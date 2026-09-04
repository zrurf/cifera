package internal

import (
	"github.com/zrurf/cifera/internal/compress"
	"github.com/zrurf/cifera/internal/vhost"
)

// Config 配置
type Config struct {
	Server      ServerConfig       `mapstructure:"server"`
	Log         LogConfig          `mapstructure:"log"`
	Addons      AddonsConfig       `mapstructure:"addons"`
	Hosts       []vhost.HostConfig `mapstructure:"hosts"`
	Compression CompressionConfig  `mapstructure:"compression"`
	Cache       CacheConfig        `mapstructure:"cache"`
	Cookies     CookiesConfig      `mapstructure:"cookies"`
	Tenants     TenantsConfig      `mapstructure:"tenants"` // 注册租户（自动租户无需预分配）
}

// ServerConfig HTTP 服务监听配置
type ServerConfig struct {
	Listen  string `mapstructure:"listen"`
	TLSCert string `mapstructure:"tls_cert"` // HTTPS 证书文件路径，与 tls_key 同时配置时启用 TLS
	TLSKey  string `mapstructure:"tls_key"`  // HTTPS 私钥文件路径
	APIHost string `mapstructure:"api_host"` // 内置 API vhost 保留主机名，空则不启用
}

// LogConfig 日志相关配置
type LogConfig struct {
	Level       string `mapstructure:"level"`
	Persistent  bool   `mapstructure:"persistent"`
	Path        string `mapstructure:"path"`
	Compression bool   `mapstructure:"compression"`
	MaxSize     int    `mapstructure:"max_size"`
	MaxAge      int    `mapstructure:"max_age"`
	MaxBackups  int    `mapstructure:"max_backups"`
}

// AddonsConfig Addon 系统配置
type AddonsConfig struct {
	Dir     string                    `mapstructure:"dir"`     // addon 加载目录
	Enabled []string                  `mapstructure:"enabled"` // 仅加载指定 addon（按 id），若为空则加载全部
	Params  map[string]map[string]any `mapstructure:"params"`  // 全局 addon 参数：addon_id → {param: value}
}

// CompressionConfig 压缩配置
type CompressionConfig struct {
	Enabled bool                     `mapstructure:"enabled"`
	Gzip    compress.AlgorithmConfig `mapstructure:"gzip"`
	Brotli  compress.AlgorithmConfig `mapstructure:"brotli"`
	Zstd    compress.AlgorithmConfig `mapstructure:"zstd"`
}

// ToCompressConfig 转换为 compress.Config
func (c CompressionConfig) ToCompressConfig() compress.Config {
	return compress.Config{
		Enabled: c.Enabled,
		Algos: map[compress.Algorithm]compress.AlgorithmConfig{
			compress.Gzip:   c.Gzip,
			compress.Brotli: c.Brotli,
			compress.Zstd:   c.Zstd,
		},
	}
}

// CacheConfig 缓存配置
type CacheConfig struct {
	Enabled bool  `mapstructure:"enabled"`
	MaxSize int64 `mapstructure:"max_size"` // 最大缓存大小（字节），默认 256MB
}

// CookiesConfig Cookie Jar 配置
type CookiesConfig struct {
	Enabled         bool   `mapstructure:"enabled"`
	JarCapacity     int    `mapstructure:"jar_capacity"`     // 每个 jar 最大 cookie 数量，默认 500
	PersistPath     string `mapstructure:"persist_path"`     // Badger 数据目录，空则不持久化
	CleanupInterval int    `mapstructure:"cleanup_interval"` // 清理间隔（秒），默认 300
}

// TenantsConfig 注册租户配置，键为租户 ID
type TenantsConfig map[string]TenantConfig

// TenantConfig 单个注册租户的配置
type TenantConfig struct {
	Name          string                    `mapstructure:"name"`
	Enabled       bool                      `mapstructure:"enabled"`
	TokenTTL      int                       `mapstructure:"token_ttl"`      // token 有效期（秒），默认 86400
	Secret        string                    `mapstructure:"secret"`         // 可选接入密钥（鉴权阶段）
	AddonParams   map[string]map[string]any `mapstructure:"addon_params"`   // addon_id → 参数覆盖
	EnabledAddons []string                  `mapstructure:"enabled_addons"` // 可选，限定租户可用 addon
}
