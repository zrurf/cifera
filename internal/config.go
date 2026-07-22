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
}

// ServerConfig HTTP服务的监听配置
type ServerConfig struct {
	Listen string `mapstructure:"listen"`
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
	Dir     string   `mapstructure:"dir"`     // addon 加载目录
	Enabled []string `mapstructure:"enabled"` // 仅加载指定 addon（按 id），若为空则加载全部
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
	PersistPath     string `mapstructure:"persist_path"`     // NutsDB 数据目录，空则不持久化
	CleanupInterval int    `mapstructure:"cleanup_interval"` // 清理间隔（秒），默认 300
}
