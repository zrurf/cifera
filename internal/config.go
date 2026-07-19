package internal

// Config 配置
type Config struct {
	Server ServerConfig `mapstructure:"server"`
	Log    LogConfig    `mapstructure:"log"`
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
