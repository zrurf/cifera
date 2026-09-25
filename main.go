package main

import (
	"context"
	"crypto/tls"
	_ "embed"
	"fmt"
	"io/fs"
	"net"
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

	addonReport, err := addon.LoadAddons(config.Addons.Dir, config.Addons.Enabled, config.Addons.Params, logger)
	if err != nil {
		logger.Fatal("加载 addon 失败", zap.Error(err))
	}
	addons := addonReport.Addons

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

	handler := internal.CreateServer(logger, ciferaRuntimeJS, addons, registry, negotiator, cch, cookieMgr, config.Server.APIHost, config.Server.Listen, tstore)

	// 监听退出信号，触发停机
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// addon 热加载：目录存在时启用。建立监听只做系统调用、同步完成，事件循环在后台运行，
	// 因此目录监听不会阻塞服务启动；日志顺序也固定为「热加载已启用」先于「服务启动」，
	// 避免再出现「日志停在 addon 相关行、看不出服务是否已启动」的误判。
	if stat, err := os.Stat(config.Addons.Dir); err == nil && stat.IsDir() {
		if reloader, ok := handler.(interface{ ReplaceAddons([]*addon.LoadedAddon) }); ok {
			watcher, err := newAddonWatcher(config.Addons.Dir, config.Addons.Enabled, config.Addons.Params, reloader.ReplaceAddons, logger)
			if err != nil {
				logger.Warn("addon 热加载未启用", zap.String("dir", config.Addons.Dir), zap.Error(err))
			} else {
				go watcher.run(ctx)
			}
		}
	}

	// 先完成端口绑定再宣告启动：日志中的「服务启动」即代表端口已就绪
	ln, err := listen(config.Server)
	if err != nil {
		logger.Fatal("服务启动失败", zap.Error(err))
	}

	// 慢请求防护：限制请求头读取时间与空闲连接保留时间
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		err := srv.Serve(ln)
		if err != nil && err != http.ErrServerClosed {
			logger.Fatal("服务异常退出", zap.Error(err))
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

// listen 创建监听器：配置了证书与私钥时返回 TLS 监听器
func listen(config internal.ServerConfig) (net.Listener, error) {
	if config.TLSCert == "" || config.TLSKey == "" {
		return net.Listen("tcp", config.Listen)
	}

	cert, err := tls.LoadX509KeyPair(config.TLSCert, config.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("加载 TLS 证书失败: %w", err)
	}

	// NextProtos 与 http.Server.ServeTLS 的默认值保持一致，保留 HTTP/2 支持
	return tls.Listen("tcp", config.Listen, &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
	})
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

// reloadDebounce 目录变更防抖时长：多次变更合并为一次重载
const reloadDebounce = 500 * time.Millisecond

// addonWatcher 监听 addon 目录树上的变更
type addonWatcher struct {
	dir     string
	enabled []string
	params  map[string]map[string]any
	replace func([]*addon.LoadedAddon)
	logger  *zap.Logger

	w       *fsnotify.Watcher
	watched map[string]bool // 已加入监听的目录（绝对路径），避免重复 Add
}

// newAddonWatcher 建立对 addon 目录及其各级子目录的监听。
// 只做建立监听所需的系统调用，事件循环交由 run 在后台执行，
// 因此目录监听不会阻塞服务启动。
func newAddonWatcher(dir string, enabled []string, globalParams map[string]map[string]any, replace func([]*addon.LoadedAddon), logger *zap.Logger) (*addonWatcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("创建文件监听器失败: %w", err)
	}

	watcher := &addonWatcher{
		dir:     dir,
		enabled: enabled,
		params:  globalParams,
		replace: replace,
		logger:  logger,
		w:       w,
		watched: make(map[string]bool),
	}

	if err := watcher.addTree(dir); err != nil {
		w.Close()
		return nil, err
	}

	// 目录树全部纳入监听后输出该日志，保证它出现在「服务启动」之前
	logger.Info("addon 热加载已启用",
		zap.String("dir", dir),
		zap.Int("watched_dirs", len(watcher.watched)),
	)
	return watcher, nil
}

// addTree 把 root 及其全部子目录（任意层级）加入监听。
// addon 资源常位于多级子目录（如 dist/），只监听一级目录会漏掉这些资源文件的变更。
// root 自身加入失败视为错误，子目录失败仅记录：子目录可能在遍历过程中被删除。
func (aw *addonWatcher) addTree(root string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("解析 addon 目录路径失败: %w", err)
	}

	return filepath.WalkDir(absRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		// 无法访问的目录跳过；root 本身失败则整体失败（此时 entry 可能为 nil）
		if walkErr != nil {
			if path == absRoot {
				return fmt.Errorf("监听 addon 目录失败: %w", walkErr)
			}
			aw.logger.Debug("跳过无法访问的 addon 子目录", zap.String("path", path), zap.Error(walkErr))
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if aw.watched[path] {
			return nil
		}
		if err := aw.w.Add(path); err != nil {
			if path == absRoot {
				return fmt.Errorf("监听 addon 目录失败: %w", err)
			}
			aw.logger.Debug("监听 addon 子目录失败", zap.String("path", path), zap.Error(err))
			return nil
		}
		aw.watched[path] = true
		return nil
	})
}

// run 处理监听事件：变更后防抖触发一次重载；ctx 取消时退出
func (aw *addonWatcher) run(ctx context.Context) {
	defer aw.w.Close()

	var mu sync.Mutex
	var timer *time.Timer
	poke := func() {
		mu.Lock()
		defer mu.Unlock()
		if timer != nil {
			return
		}
		timer = time.AfterFunc(reloadDebounce, func() {
			mu.Lock()
			timer = nil
			mu.Unlock()
			aw.reload()
		})
	}

	for {
		select {
		case <-ctx.Done():
			aw.logger.Info("addon 热加载已停止")
			return
		case ev, ok := <-aw.w.Events:
			if !ok {
				return
			}
			// 新建目录（含多级）需纳入监听，否则其内部变更不会触发重载
			if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
				_ = aw.addTree(ev.Name)
			}
			poke()
		case err, ok := <-aw.w.Errors:
			if !ok {
				return
			}
			aw.logger.Debug("addon 目录监听异常", zap.Error(err))
		}
	}
}

// reload 重新加载 addon 并原子替换运行时列表
func (aw *addonWatcher) reload() {
	report, err := addon.LoadAddons(aw.dir, aw.enabled, aw.params, aw.logger)
	if err != nil {
		aw.logger.Error("addon 热加载失败", zap.Error(err))
		return
	}

	// 目录处于构建中间态时（如 addon 资源文件正在重建）加载会失败，
	// 此时清空运行时列表会让服务在没有 addon 的状态下继续运行，故保留现有列表
	if len(report.Addons) == 0 && report.Failed > 0 {
		aw.logger.Warn("addon 热更新未产生可用 addon，保留现有列表",
			zap.Int("failed", report.Failed),
		)
		return
	}

	aw.replace(report.Addons)
	aw.logger.Info("addon 已热更新",
		zap.Int("count", len(report.Addons)),
		zap.Int("failed", report.Failed),
	)
}
