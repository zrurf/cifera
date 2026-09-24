package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/zrurf/cifera/internal"
	"github.com/zrurf/cifera/internal/addon"
	"go.uber.org/zap"
)

// initConfigForTest 以指定命令行参数初始化配置（每次重置全局 FlagSet 避免状态泄漏）
func initConfigForTest(t *testing.T, args []string) (*internal.Config, error) {
	t.Helper()
	pflag.CommandLine = pflag.NewFlagSet("test", pflag.ContinueOnError)
	oldArgs := os.Args
	os.Args = append([]string{"cifera"}, args...)
	t.Cleanup(func() { os.Args = oldArgs })

	return initConfig(zap.NewNop())
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// 回归测试：配置文件中的值必须优先于 flag 默认值（修复无条件 BindPFlag 导致配置文件失效的缺陷）
func TestConfigFileOverridesFlagDefault(t *testing.T) {
	cfgPath := writeConfigFile(t, "[server]\nlisten = \":9999\"\n")
	cfg, err := initConfigForTest(t, []string{"--config", cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":9999" {
		t.Fatalf("配置文件 listen 应生效，实际 %q（flag 默认值不应覆盖配置文件）", cfg.Server.Listen)
	}
}

// 命令行 flag 应覆盖配置文件
func TestConfigFlagOverridesFile(t *testing.T) {
	cfgPath := writeConfigFile(t, "[server]\nlisten = \":9999\"\n")
	cfg, err := initConfigForTest(t, []string{"--config", cfgPath, "--server.listen", ":7777"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":7777" {
		t.Fatalf("命令行 flag 应覆盖配置文件，实际 %q", cfg.Server.Listen)
	}
}

// 环境变量应覆盖配置文件（但低于命令行 flag）
func TestConfigEnvOverridesFile(t *testing.T) {
	t.Setenv("CIFERA_SERVER_LISTEN", ":8888")
	cfgPath := writeConfigFile(t, "[server]\nlisten = \":9999\"\n")
	cfg, err := initConfigForTest(t, []string{"--config", cfgPath})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":8888" {
		t.Fatalf("环境变量应覆盖配置文件，实际 %q", cfg.Server.Listen)
	}
}

// 配置文件缺失时回退到默认值
func TestConfigMissingFileUsesDefaults(t *testing.T) {
	cfg, err := initConfigForTest(t, []string{"--config", filepath.Join(t.TempDir(), "nope.toml")})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != ":80" {
		t.Fatalf("无配置文件时应用默认 listen，实际 %q", cfg.Server.Listen)
	}
	if !cfg.Log.Persistent {
		t.Error("log.persistent 默认值应为 true")
	}
}

// 配置文件存在但语法错误时应直接报错，而非静默忽略
func TestConfigParseErrorFails(t *testing.T) {
	badPath := writeConfigFile(t, "[server\nlisten = ")
	_, err := initConfigForTest(t, []string{"--config", badPath})
	if err == nil {
		t.Fatal("TOML 语法错误的配置文件应返回错误")
	}
}

// 回归测试：watchAddonsDir 必须随 ctx 取消返回。
// 该函数曾是 main 中的同步调用，导致进程卡在监听循环、服务永不启动。
func TestWatchAddonsDirStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	writeTestAddon(t, dir, "demo.cifera")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	reloaded := make(chan []*addon.LoadedAddon, 1)

	go func() {
		defer close(done)
		watchAddonsDir(ctx, dir, nil, nil, func(a []*addon.LoadedAddon) {
			select {
			case reloaded <- a:
			default:
			}
		}, zap.NewNop())
	}()

	// 变更目录内容触发一次热重载；fsnotify 监听可能晚于测试写入，
	// 故在超时前重复触发，避免依赖固定 sleep 造成偶发失败
	var loaded []*addon.LoadedAddon
	deadline := time.After(15 * time.Second)
	for loaded == nil {
		writeTestAddon(t, dir, "second.cifera")
		select {
		case got := <-reloaded:
			if len(got) == 2 {
				loaded = got
			}
		case <-time.After(2 * time.Second):
		case <-deadline:
			t.Fatal("超时未收到热重载回调")
		}
	}

	seen := make(map[string]bool, len(loaded))
	for _, a := range loaded {
		seen[a.Manifest.Addon.ID] = true
	}
	if !seen["demo.cifera"] || !seen["second.cifera"] {
		t.Fatalf("热重载结果缺少预期 addon: %v", seen)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx 取消后 watchAddonsDir 未返回")
	}
}

// writeTestAddon 在 dir 下写入一个指定 id 的最小可加载 addon
func writeTestAddon(t *testing.T, dir, id string) {
	t.Helper()
	addonDir := filepath.Join(dir, strings.Split(id, ".")[0])
	if err := os.MkdirAll(addonDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "[addon]\nid = \"" + id + "\"\nname = \"Test\"\nversion = \"1.0.0\"\n\n" +
		"[[rules]]\npattern = [\"example\\\\.com/.*\"]\naction = \"block\"\nstatus_code = 404\n"
	if err := os.WriteFile(filepath.Join(addonDir, "addon.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}
