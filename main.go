package main

import (
	"context"
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"github.com/zrurf/cifera/internal"
	"github.com/zrurf/cifera/internal/addon"
	"github.com/zrurf/cifera/internal/cache"
	"github.com/zrurf/cifera/internal/compress"
	"github.com/zrurf/cifera/internal/cookiejar"
	"github.com/zrurf/cifera/internal/tenant"
	"github.com/zrurf/cifera/internal/vhost"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

//go:embed scripts/dist/cifera.runtime.js
var ciferaRuntimeJS string

func main() {
	// 读取配置前使用基础 Logger
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

	addons, err := addon.LoadAddons(config.Addons.Dir, config.Addons.Enabled, config.Addons.Params, logger)
	if err != nil {
		logger.Fatal("加载 addon 失败", zap.Error(err))
	}

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

	var negotiator *compress.Negotiator
	if config.Compression.Enabled {
		negotiator = compress.NewNegotiator(config.Compression.ToCompressConfig())
		logger.Info("压缩模块已启用",
			zap.Bool("gzip", config.Compression.Gzip.Enabled),
			zap.Bool("brotli", config.Compression.Brotli.Enabled),
			zap.Bool("zstd", config.Compression.Zstd.Enabled),
		)
	} else {
		logger.Info("压缩模块已禁用")
	}

	var cch *cache.Cache
	if config.Cache.Enabled {
		cch = cache.New(cache.Config{
			Enabled: true,
			MaxSize: config.Cache.MaxSize,
		}, logger)
		logger.Info("缓存模块已启用",
			zap.Int64("max_size_mb", config.Cache.MaxSize/(1024*1024)),
		)
	} else {
		logger.Info("缓存模块已禁用")
	}

	var cookieMgr *cookiejar.Manager
	if config.Cookies.Enabled {
		cleanupInterval := time.Duration(config.Cookies.CleanupInterval) * time.Second
		if cleanupInterval <= 0 {
			cleanupInterval = 5 * time.Minute
		}

		cookieMgr, err = cookiejar.NewManager(cookiejar.Config{
			Enabled:         true,
			JarCapacity:     config.Cookies.JarCapacity,
			PersistPath:     config.Cookies.PersistPath,
			CleanupInterval: cleanupInterval,
		}, logger)
		if err != nil {
			logger.Fatal("初始化 Cookie Jar 管理器失败", zap.Error(err))
		}
		cookieMgr.StartCleanup()

		logger.Info("Cookie Jar 模块已启用",
			zap.Int("jar_capacity", config.Cookies.JarCapacity),
			zap.String("persist_path", config.Cookies.PersistPath),
			zap.Duration("cleanup_interval", cleanupInterval),
		)
		defer cookieMgr.Close()
	} else {
		logger.Info("Cookie Jar 模块已禁用")
	}

	// 注册租户（自动租户无需预分配）
	var tstore *tenant.Store
	if len(config.Tenants) > 0 {
		regs := make([]tenant.Registration, 0, len(config.Tenants))
		for id, tc := range config.Tenants {
			regs = append(regs, tenant.Registration{
				ID:            id,
				Name:          tc.Name,
				Enabled:       tc.Enabled,
				TokenTTL:      time.Duration(tc.TokenTTL) * time.Second,
				Secret:        tc.Secret,
				AddonParams:   tc.AddonParams,
				EnabledAddons: tc.EnabledAddons,
			})
		}
		tstore = tenant.NewStore(regs)
		logger.Info("注册租户已启用", zap.Int("count", len(regs)))
	}

	handler := internal.CreateServer(logger, ciferaRuntimeJS, addons, registry, negotiator, cch, cookieMgr, config.Server.APIHost, tstore)

	// addon 热加载：目录存在时监听变更并自动重新加载
	if stat, err := os.Stat(config.Addons.Dir); err == nil && stat.IsDir() {
		if reloader, ok := handler.(interface{ ReplaceAddons([]*addon.LoadedAddon) }); ok {
			watchAddonsDir(config.Addons.Dir, config.Addons.Enabled, config.Addons.Params, reloader.ReplaceAddons, logger)
		}
	}

	// 慢请求防护：限制请求头读取时间与空闲连接保留时间
	srv := &http.Server{
		Addr:              config.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// 监听退出信号，触发停机
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		var err error
		if config.Server.TLSCert != "" && config.Server.TLSKey != "" {
			err = srv.ListenAndServeTLS(config.Server.TLSCert, config.Server.TLSKey)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			logger.Fatal("服务启动失败", zap.Error(err))
		}
	}()

	logger.Info("服务启动", zap.String("listen", config.Server.Listen))
	<-ctx.Done()
	logger.Info("收到退出信号，正在停止服务")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("停机超时", zap.Error(err))
	}
	if cch != nil {
		cch.Close()
	}
}

// initConfig 初始化配置，优先级从高到低：命令行 flag > 环境变量 > 配置文件 > 默认值
func initConfig(logger *zap.Logger) (*internal.Config, error) {
	var cfg internal.Config
	initFlag()
	pflag.Parse()

	// 初始化 Viper 前需要配置文件路径，先从 pflag 手动获取
	configFile, _ := pflag.CommandLine.GetString("config")

	v := viper.New()
	syncDefaults(v)
	setDefaults(v)

	v.SetConfigFile(configFile)
	if err := v.ReadInConfig(); err != nil {
		// 显式指定路径时 viper 对"文件不存在"返回 *os.PathError，
		// 需用 os.IsNotExist 判断而不能依赖 viper.ConfigFileNotFoundError
		if os.IsNotExist(err) {
			logger.Info("未找到配置文件，将使用默认值和环境变量", zap.String("path", configFile))
		} else {
			// 配置文件存在但解析失败（如 TOML 语法错误），直接报错退出而非静默忽略
			return nil, fmt.Errorf("读取配置文件 '%s' 失败: %w", configFile, err)
		}
	}

	v.SetEnvPrefix("CIFERA")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// 仅绑定用户在命令行显式传入的 flag。
	// 若无条件绑定，pflag 默认值会覆盖配置文件中的值，导致配置文件失效。
	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		if f.Name != "config" && f.Changed {
			_ = v.BindPFlag(f.Name, f)
		}
	})

	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}

	return &cfg, nil
}

// initFlag 定义命令行 flag
func initFlag() {
	// 幂等：重复初始化（如测试中多次调用）时不重复注册，避免 panic
	if pflag.Lookup("config") != nil {
		return
	}
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

// syncDefaults 将 pflag 默认值同步为 Viper 默认值，使默认值只定义一次
func syncDefaults(v *viper.Viper) {
	pflag.CommandLine.VisitAll(func(f *pflag.Flag) {
		if f.Name != "config" {
			v.SetDefault(f.Name, f.DefValue)
		}
	})
}

// setDefaults 设置无对应命令行 flag 的配置项默认值
func setDefaults(v *viper.Viper) {
	v.SetDefault("addons.dir", "./addons")

	v.SetDefault("compression.enabled", true)
	v.SetDefault("compression.gzip.enabled", true)
	v.SetDefault("compression.gzip.level", 5)
	v.SetDefault("compression.brotli.enabled", true)
	v.SetDefault("compression.brotli.level", 4)
	v.SetDefault("compression.zstd.enabled", true)
	v.SetDefault("compression.zstd.level", 3)

	v.SetDefault("cache.enabled", true)
	v.SetDefault("cache.max_size", 256*1024*1024) // 256MB

	v.SetDefault("cookies.enabled", true)
	v.SetDefault("cookies.jar_capacity", 500)
	v.SetDefault("cookies.persist_path", "./data/cookies")
	v.SetDefault("cookies.cleanup_interval", 300) // 秒
}

// initLog 根据配置初始化日志系统并替换全局 logger
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

	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderConfig),
		zapcore.AddSync(os.Stdout),
		l,
	)

	core := consoleCore
	if config.Persistent {
		// 确保日志目录存在，创建失败则退化为仅控制台输出
		logDir := filepath.Dir(config.Path)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			baseLogger.Error("创建日志目录失败，仅使用控制台输出", zap.String("path", logDir), zap.Error(err))
		} else {
			lumberJackLogger := &lumberjack.Logger{
				Filename:   config.Path,
				MaxSize:    config.MaxSize,
				MaxBackups: config.MaxBackups,
				MaxAge:     config.MaxAge,
				Compress:   config.Compression,
			}
			fileCore := zapcore.NewCore(
				zapcore.NewJSONEncoder(encoderConfig),
				zapcore.AddSync(lumberJackLogger),
				l,
			)
			core = zapcore.NewTee(fileCore, consoleCore)
		}
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

// watchAddonsDir 监听 addon 目录及其一级子目录，变更后防抖触发重载
func watchAddonsDir(dir string, enabled []string, globalParams map[string]map[string]any, replace func([]*addon.LoadedAddon), logger *zap.Logger) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Warn("初始化 addon 热加载失败", zap.Error(err))
		return
	}
	defer w.Close()

	if err := w.Add(dir); err != nil {
		logger.Warn("监听 addon 目录失败", zap.String("dir", dir), zap.Error(err))
		return
	}
	watchAddonsSubdirs(w, dir)
	logger.Info("addon 热加载已启用", zap.String("dir", dir))

	var mu sync.Mutex
	var timer *time.Timer
	// 防抖：多次变更合并为一次重载
	poke := func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			return
		}
		timer = time.AfterFunc(500*time.Millisecond, func() {
			mu.Lock()
			timer = nil
			mu.Unlock()
			reloadAddons(dir, enabled, globalParams, replace, logger)
		})
	}

	for {
		select {
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
				_ = w.Add(ev.Name)
			}
			poke()
		case err, ok := <-w.Errors:
			if !ok {
				return
			}
			logger.Debug("addon 目录监听异常", zap.Error(err))
		}
	}
}

// watchAddonsSubdirs 将 addon 目录下的一级子目录加入监听
func watchAddonsSubdirs(w *fsnotify.Watcher, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			_ = w.Add(filepath.Join(dir, e.Name()))
		}
	}
}

// reloadAddons 重新加载 addon 并原子替换运行时列表
func reloadAddons(dir string, enabled []string, globalParams map[string]map[string]any, replace func([]*addon.LoadedAddon), logger *zap.Logger) {
	addons, err := addon.LoadAddons(dir, enabled, globalParams, logger)
	if err != nil {
		logger.Error("addon 热加载失败", zap.Error(err))
		return
	}
	replace(addons)
	logger.Info("addon 已热更新", zap.Int("count", len(addons)))
}
