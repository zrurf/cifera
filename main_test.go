package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
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

// 回归测试：addon 监听必须随 ctx 取消返回。
// 该监听曾是 main 中的同步调用，导致进程卡在监听循环、服务永不启动。
func TestAddonWatcherStopsOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	writeTestAddon(t, dir, "demo.cifera")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	reloaded := make(chan []*addon.LoadedAddon, 1)

	watcher, err := newAddonWatcher(dir, nil, nil, func(a []*addon.LoadedAddon) {
		select {
		case reloaded <- a:
		default:
		}
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("建立 addon 监听失败: %v", err)
	}

	go func() {
		defer close(done)
		watcher.run(ctx)
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
		t.Fatal("ctx 取消后 addon 监听未返回")
	}
}

// 回归测试：addon 资源位于多级子目录（如 dist/）时，其变更同样必须触发热重载。
// 此前只监听 addon 目录及其一级子目录，dist/*.js 的修改不会触发重载。
func TestAddonWatcherReloadsOnNestedChange(t *testing.T) {
	dir := t.TempDir()
	writeTestAddon(t, dir, "demo.cifera")

	nested := filepath.Join(dir, "demo", "dist")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	resource := filepath.Join(nested, "resource.js")
	if err := os.WriteFile(resource, []byte("// v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reloaded := make(chan struct{}, 1)
	watcher, err := newAddonWatcher(dir, nil, nil, func([]*addon.LoadedAddon) {
		select {
		case reloaded <- struct{}{}:
		default:
		}
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("建立 addon 监听失败: %v", err)
	}
	go watcher.run(ctx)

	if !watcher.watched[nested] {
		t.Fatalf("多级子目录未被加入监听: %v", watcher.watched)
	}

	deadline := time.After(15 * time.Second)
	for {
		if err := os.WriteFile(resource, []byte("// v2"), 0o644); err != nil {
			t.Fatal(err)
		}
		select {
		case <-reloaded:
			return
		case <-time.After(2 * time.Second):
		case <-deadline:
			t.Fatal("多级子目录中的资源变更未触发热重载")
		}
	}
}

// 回归测试：全部 addon 加载失败（如资源文件正在重建）时不得清空运行时 addon 列表，
// 否则服务会在没有任何 addon 的状态下继续运行。
func TestAddonWatcherKeepsAddonsWhenReloadFailsEntirely(t *testing.T) {
	dir := t.TempDir()

	// 规则声明的资源文件不存在 → 该 addon 加载失败
	if err := os.MkdirAll(filepath.Join(dir, "broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := "[addon]\nid = \"broken.cifera\"\nname = \"Broken\"\nversion = \"1.0.0\"\n\n" +
		"[[rules]]\npattern = [\"example\\\\.com/.*\"]\naction = \"replace_content\"\nresource = \"dist/missing.js\"\n"
	if err := os.WriteFile(filepath.Join(dir, "broken", "addon.toml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	replaced := false
	watcher, err := newAddonWatcher(dir, nil, nil, func([]*addon.LoadedAddon) {
		replaced = true
	}, zap.NewNop())
	if err != nil {
		t.Fatalf("建立 addon 监听失败: %v", err)
	}

	watcher.reload()
	if replaced {
		t.Fatal("全部 addon 加载失败时不应替换运行时列表")
	}
}

// 配置了证书与私钥时，listen 返回的 TLS 监听器应能直接交给 http.Server 提供服务
func TestListenWithTLS(t *testing.T) {
	// 复用 httptest 生成的自签证书，避免测试中重复实现证书生成
	ts := httptest.NewTLSServer(http.NotFoundHandler())
	defer ts.Close()
	leaf := ts.TLS.Certificates[0]

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	writePEM(t, certPath, "CERTIFICATE", leaf.Certificate[0])
	keyDER, err := x509.MarshalPKCS8PrivateKey(leaf.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, keyPath, "PRIVATE KEY", keyDER)

	ln, err := listen(internal.ServerConfig{Listen: "127.0.0.1:0", TLSCert: certPath, TLSKey: keyPath})
	if err != nil {
		t.Fatalf("创建 TLS 监听器失败: %v", err)
	}
	defer ln.Close()

	tlsStateSeen := false
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tlsStateSeen = r.TLS != nil
		io.WriteString(w, "ok")
	})}
	go srv.Serve(ln)
	defer srv.Close()

	resp, err := ts.Client().Get("https://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("TLS 请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d，期望 200", resp.StatusCode)
	}
	if !tlsStateSeen {
		t.Error("服务端请求缺少 TLS 连接状态")
	}
}

// writePEM 以指定块类型写出 PEM 文件
func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if data == nil {
		t.Fatalf("PEM 编码失败: %s", blockType)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
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
