package internal

import "github.com/zrurf/cifera/internal/vhost"

// Config 配置
type Config struct {
	Server ServerConfig       `mapstructure:"server"`
	Log    LogConfig          `mapstructure:"log"`
	Addons AddonsConfig       `mapstructure:"addons"`
	Hosts  []vhost.HostConfig `mapstructure:"hosts"`
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
