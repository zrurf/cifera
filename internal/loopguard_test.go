package internal

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zrurf/cifera/internal/vhost"
	"go.uber.org/zap"
)

// testRecursionDepth 测试内的递归深度上限：回环防护失效时把递归限制在可控范围内，
// 使测试以断言失败（而非测试进程内存耗尽）结束
const testRecursionDepth = 32

// recursionProbe 记录 handler 的并发进入层数峰值，用于断言请求未被递归转发
type recursionProbe struct {
	mu   sync.Mutex
	cur  int
	peak int
}

func (p *recursionProbe) enter() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cur++
	if p.cur > p.peak {
		p.peak = p.cur
	}
	return p.cur
}

func (p *recursionProbe) leave() {
	p.mu.Lock()
	p.cur--
	p.mu.Unlock()
}

func (p *recursionProbe) peakDepth() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak
}

// withRecursionLimit 限制 handler 的并发进入深度，仅用于测试兜底
func withRecursionLimit(h http.Handler, p *recursionProbe) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.enter() > testRecursionDepth {
			p.leave()
			http.Error(w, "test: recursion depth exceeded", http.StatusInternalServerError)
			return
		}
		defer p.leave()
		h.ServeHTTP(w, r)
	})
}

// 回归测试：以自身监听地址为目标时，请求必须当场被拒绝，而不是转发给自己。
// 修复前该请求会不断自我转发，单次请求即可让连接数与内存暴涨。
func TestSelfTargetRequestRejected(t *testing.T) {
	// 先占端口再建 handler，使回环判定使用真实监听地址
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listenAddr := ln.Addr().String()

	probe := &recursionProbe{}
	handler := CreateServer(zap.NewNop(), "", nil, vhost.NewRegistry(zap.NewNop()), nil, nil, nil, "", listenAddr, nil)
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: withRecursionLimit(handler, probe)}}
	srv.Start()
	defer srv.Close()

	client := srv.Client()
	client.Timeout = 10 * time.Second

	// 默认 Host 即服务自身地址，等价于浏览器直接访问代理
	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("请求不应挂起或失败，实际错误: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusLoopDetected {
		t.Fatalf("目标为自身时应返回 508，实际 %d", resp.StatusCode)
	}
	// 峰值层数为 1 表示请求未发生自我转发（仅靠跳数上限兜底会达到 10 层以上）
	if got := probe.peakDepth(); got != 1 {
		t.Fatalf("目标为自身的请求不应被转发，峰值处理层数应为 1，实际 %d", got)
	}
}

// 端口不同不应判定为回环：本机其他端口由其他服务监听
func TestLoopGuardDifferentPortIsNotSelf(t *testing.T) {
	g := newLoopGuard(":8080")
	if g.IsSelf(context.Background(), "127.0.0.1:9090", "http") {
		t.Error("同机不同端口不应判定为本代理自身")
	}
	if !g.IsSelf(context.Background(), "127.0.0.1:8080", "http") {
		t.Error("同机同端口应判定为本代理自身")
	}
}

// 回环场景：本机地址的字面量形式（含缺省端口推断）
func TestLoopGuardDetectsLocalLiterals(t *testing.T) {
	g := newLoopGuard(":8080")

	cases := []string{
		"127.0.0.1:8080",
		"127.0.0.2:8080", // 整个 127.0.0.0/8 均为回环
		"localhost:8080",
		"LOCALHOST:8080",
		"foo.localhost:8080",
		"[::1]:8080",
		"0.0.0.0:8080", // 未指定地址等价于本机
	}
	for _, host := range cases {
		if !g.IsSelf(context.Background(), host, "http") {
			t.Errorf("%q 应判定为本代理自身", host)
		}
	}

	// 缺省端口按 schema 推断：监听 :80 时无端口的回环地址同样命中
	g80 := newLoopGuard(":80")
	if !g80.IsSelf(context.Background(), "127.0.0.1", "http") {
		t.Error("监听 :80 时 127.0.0.1 应判定为自身")
	}
	if g80.IsSelf(context.Background(), "127.0.0.1:8080", "http") {
		t.Error("监听 :80 时 127.0.0.1:8080 不应判定为自身")
	}
}

// 监听在具体地址时仅该地址属于本代理，同端口的其他地址应放行
func TestLoopGuardBoundAddressIsPrecise(t *testing.T) {
	g := newLoopGuard("192.0.2.10:8080") // TEST-NET-1，不会出现在本机网卡上

	if !g.IsSelf(context.Background(), "192.0.2.10:8080", "http") {
		t.Error("监听地址自身应判定为本代理")
	}
	if g.IsSelf(context.Background(), "192.0.2.11:8080", "http") {
		t.Error("非监听地址不应判定为本代理")
	}
}

// 本机网卡地址（如局域网 IP）同样应判定为自身
func TestLoopGuardDetectsInterfaceAddress(t *testing.T) {
	var lanIP net.IP
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("无法枚举网卡地址: %v", err)
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || ipNet.IP.To4() == nil {
			continue
		}
		lanIP = ipNet.IP
		break
	}
	if lanIP == nil {
		t.Skip("本机无非回环 IPv4 地址")
	}

	g := newLoopGuard(":8080")
	if !g.IsSelf(context.Background(), net.JoinHostPort(lanIP.String(), "8080"), "http") {
		t.Errorf("本机网卡地址 %s 应判定为本代理自身", lanIP)
	}
}

// 监听地址为空时无法判定，回环检查应整体禁用（fail-open，不影响转发）
func TestLoopGuardDisabledWithoutListen(t *testing.T) {
	g := newLoopGuard("")
	if g.IsSelf(context.Background(), "127.0.0.1:8080", "http") {
		t.Error("监听地址为空时应禁用回环检查")
	}
}

// Max-Forwards：携带时递减、不得超过本代理上限、耗尽后拒绝
func TestNextMaxForwards(t *testing.T) {
	if got := nextMaxForwards(""); got != maxForwardsLimit-1 {
		t.Errorf("未携带时应从本代理上限起算，实际 %d", got)
	}
	if got := nextMaxForwards("3"); got != 2 {
		t.Errorf("携带 3 时应递减为 2，实际 %d", got)
	}
	if got := nextMaxForwards("0"); got >= 0 {
		t.Errorf("携带 0 时应返回负值以拒绝转发，实际 %d", got)
	}
	if got := nextMaxForwards("abc"); got != maxForwardsLimit-1 {
		t.Errorf("非法值应回落到本代理上限，实际 %d", got)
	}
	if got := nextMaxForwards("999999"); got >= maxForwardsLimit {
		t.Errorf("超过上限的值应被封顶，实际 %d", got)
	}
}

// 跳数耗尽的请求应被拒绝，且不得发起任何转发
func TestMaxForwardsExhaustedRejected(t *testing.T) {
	handler := CreateServer(zap.NewNop(), "", nil, vhost.NewRegistry(zap.NewNop()), nil, nil, nil, "", ":0", nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/?"+proxyHostQuery("example.com"), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Max-Forwards", "0")

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusLoopDetected {
		t.Fatalf("Max-Forwards 耗尽时应返回 508，实际 %d", resp.StatusCode)
	}
}

// 出站请求应携带递减后的 Max-Forwards，使下游代理能继续限制跳数
func TestOutboundCarriesMaxForwards(t *testing.T) {
	var gotHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("Max-Forwards")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	host := strings.TrimPrefix(upstream.URL, "http://")
	handler := CreateServer(zap.NewNop(), "", nil, vhost.NewRegistry(zap.NewNop()), nil, nil, nil, "", ":0", nil)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/?" + proxyHostQuery(host))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("转发上游应成功，实际 %d", resp.StatusCode)
	}
	if gotHeader != strconv.Itoa(maxForwardsLimit-1) {
		t.Errorf("出站 Max-Forwards 应为 %d，实际 %q", maxForwardsLimit-1, gotHeader)
	}
}

// proxyHostQuery 构造指向目标 host 的代理查询串
func proxyHostQuery(host string) string {
	return "_cifera_h=" + host + "&_cifera_s=http"
}
