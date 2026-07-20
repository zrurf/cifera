package main

import (
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/zrurf/cifera/internal"
	"github.com/zrurf/cifera/internal/addon"
	"github.com/zrurf/cifera/internal/cache"
	"github.com/zrurf/cifera/internal/compress"
	"github.com/zrurf/cifera/internal/vhost"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

//go:embed scripts/dist/cifera.runtime.js
var ciferaRuntimeJS string

func main() {
	// 初始化基础 Logger 以捕获启动阶段的错误
	logger, err := zap.NewProduction()
	if err != nil {
		os.Exit(1)
	}

	config, err := initConfig(logger)
	if err != nil {
		logger.Fatal("无法读取配置", zap.Error(err))
	}

	logger = initLog(logger, config.Log)
	defer logger.Sync()

	// 加载 addon
	addons, err := addon.LoadAddons(config.Addons.Dir, config.Addons.Enabled, logger)
	if err != nil {
		logger.Fatal("加载 addon 失败", zap.Error(err))
	}

	// 初始化虚拟主机注册表
	registry := vhost.NewRegistry(logger)
	if err := registry.LoadFromConfig(config.Hosts); err != nil {
		logger.Fatal("加载 config 虚拟主机失败", zap.Error(err))
	}
	for _, a := range addons {
		if err := registry.LoadFromAddonHosts(a.Manifest.Hosts, a.Dir, a.Manifest.Addon.ID); err != nil {
			logger.Error("加载 addon 虚拟主机失败",
				zap.String("addon", a.Manifest.Addon.ID),
				zap.Error(err),
			)
		}
	}

	// 初始化压缩协商器
	var negotiator *compress.Negotiator
	if config.Compression.Enabled {
		compressCfg := config.Compression.ToCompressConfig()
		negotiator = compress.NewNegotiator(compressCfg)
		logger.Info("压缩模块已启用",
			zap.Bool("gzip", config.Compression.Gzip.Enabled),
			zap.Bool("brotli", config.Compression.Brotli.Enabled),
			zap.Bool("zstd", config.Compression.Zstd.Enabled),
		)
	} else {
		logger.Info("压缩模块已禁用")
	}

	// 初始化缓存
	var cch *cache.Cache
	if config.Cache.Enabled {
		cch = cache.New(cache.Config{
			Enabled: true,
			MaxSize: config.Cache.MaxSize,
		}, logger)
		maxSizeMB := config.Cache.MaxSize / (1024 * 1024)
		logger.Info("缓存模块已启用",
			zap.Int64("max_size_mb", maxSizeMB),
		)
	} else {
		logger.Info("缓存模块已禁用")
	}

	handler := internal.CreateServer(logger, ciferaRuntimeJS, addons, registry, negotiator, cch)
	http.Handle("/", handler)

	logger.Info("服务启动", zap.String("listen", config.Server.Listen))
	if err := http.ListenAndServe(config.Server.Listen, nil); err != nil {
		logger.Fatal("服务启动失败", zap.Error(err))
	}
}

func initConfig(logger *zap.Logger) (*internal.Config, error) {
	var cfg internal.Config
	initFlag()

	pflag.Parse()

	v := viper.New()

	setDefaults(v)

	configFile, _ := pflag.CommandLine.GetString("config")
	v.SetConfigFile(configFile)

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			logger.Info("未找到配置文件，将使用默认值和环境变量")
		} else {
			logger.Error("读取配置文件失败", zap.Error(err))
		}
	}

	v.SetEnvPrefix("CIFERA")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		if f.Name != "config" {
			_ = v.BindPFlag(f.Name, f)
		}
	})

	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}

	return &cfg, nil
}

func initFlag() {
	pflag.String("config", "./config.toml", "配置文件路径")
	pflag.String("server.listen", ":80", "HTTP 监听地址")
	pflag.String("log.path", "./log/server.log", "日志文件路径")
	pflag.String("log.level", "info", "日志级别")
	pflag.Int("log.max_size", 50, "日志文件最大大小 (MB)")
	pflag.Int("log.max_backups", 3, "日志文件最大备份数")
	pflag.Int("log.max_age", 7, "日志文件最大保留天数")
	pflag.Bool("log.compression", true, "启用日志文件压缩")
	pflag.Bool("log.persistent", true, "持久化日志文件")
}

func setDefaults(v *viper.Viper) {
	// Server 默认值
	v.SetDefault("server.listen", ":80")

	// Log 默认值
	v.SetDefault("log.level", "info")
	v.SetDefault("log.path", "./log/latest.log")
	v.SetDefault("log.persistent", true)

	// Lumberjack 默认值
	v.SetDefault("log.max_size", 50) // MB
	v.SetDefault("log.max_backups", 3)
	v.SetDefault("log.max_age", 7) // days
	v.SetDefault("log.compression", true)

	// Addons 默认值
	v.SetDefault("addons.dir", "./addons")

	// Compression 默认值
	v.SetDefault("compression.enabled", true)
	v.SetDefault("compression.gzip.enabled", true)
	v.SetDefault("compression.gzip.level", 5)
	v.SetDefault("compression.brotli.enabled", true)
	v.SetDefault("compression.brotli.level", 4)
	v.SetDefault("compression.zstd.enabled", true)
	v.SetDefault("compression.zstd.level", 3)

	// Cache 默认值
	v.SetDefault("cache.enabled", true)
	v.SetDefault("cache.max_size", 256*1024*1024) // 256MB
}

// initLog 根据配置初始化日志系统，返回配置完成后的 logger
func initLog(baseLogger *zap.Logger, config internal.LogConfig) *zap.Logger {
	var l zapcore.Level
	if err := l.UnmarshalText([]byte(config.Level)); err != nil {
		baseLogger.Warn("日志级别解析失败，使用默认 info 级别", zap.Error(err))
		l = zapcore.InfoLevel
	}

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	var core zapcore.Core
	consoleWriter := zapcore.AddSync(os.Stdout)
	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		consoleWriter,
		l,
	)

	if config.Persistent {
		lumberJackLogger := &lumberjack.Logger{
			Filename:   config.Path,
			MaxSize:    config.MaxSize,
			MaxBackups: config.MaxBackups,
			MaxAge:     config.MaxAge,
			Compress:   config.Compression,
		}
		fileWriter := zapcore.AddSync(lumberJackLogger)
		fileCore := zapcore.NewCore(
			zapcore.NewJSONEncoder(encoderConfig),
			fileWriter,
			l,
		)
		core = zapcore.NewTee(fileCore, consoleCore)
	} else {
		core = consoleCore
	}

	newLogger := zap.New(
		core,
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)

	// 替换全局 logger，确保 zap.L() 也能获取正确实例
	zap.ReplaceGlobals(newLogger)

	return newLogger
}
