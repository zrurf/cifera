package internal

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 回环防护。
// 目标地址指向本代理自身监听端口时，转发会让请求重新进入本进程：每跳新增一条连接、
// 一份读写缓冲与一个 X-Forwarded-For 条目，连接数与内存随跳数放大，单个请求即可耗尽资源。
// 因此转向前先判定目标是否为本代理自身，并用 Max-Forwards 限制转发跳数兜底。
const (
	// maxForwardsLimit 本代理允许的转发跳数上限（对客户端携带值同样封顶，避免被畸形值绕过）
	maxForwardsLimit = 10
	// loopAddrTTL 本机地址集合缓存时长（网卡地址可能随 DHCP 变化）
	loopAddrTTL = 30 * time.Second
	// loopDNSTTL 目标域名解析结果缓存时长（含解析失败）
	loopDNSTTL = 60 * time.Second
	// loopDNSTimeout 回环判定专用解析超时，超时按"非本机"处理，不阻塞转发
	loopDNSTimeout = 3 * time.Second
	// loopDNSCacheMax 解析缓存条目上限，避免大量不同域名使缓存无界增长
	loopDNSCacheMax = 512
)

// loopGuard 判定代理目标是否指向本代理自身的监听地址
type loopGuard struct {
	port    string // 监听端口；为空表示无法判定，此时禁用回环检查
	boundIP net.IP // 监听在具体地址时仅该地址属于本代理；通配监听为 nil

	mu         sync.RWMutex
	localAddrs map[string]bool
	localAt    time.Time
	dnsCache   map[string]loopDNSEntry
}

// loopDNSEntry 域名解析缓存条目
type loopDNSEntry struct {
	addrs []net.IP
	at    time.Time
}

// newLoopGuard 依据监听地址创建回环判定器；listen 形如 ":8080"、"127.0.0.1:8080"
func newLoopGuard(listen string) *loopGuard {
	g := &loopGuard{dnsCache: make(map[string]loopDNSEntry)}

	addr := strings.TrimSpace(listen)
	if addr == "" {
		return g
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		// 兼容只写端口的形式（"8080"）；其余无法解析的配置禁用检查（fail-open，不影响转发）
		if _, convErr := strconv.Atoi(addr); convErr == nil {
			g.port = addr
		}
		return g
	}
	g.port = port

	// 监听在具体地址时，同端口的其他本机地址由其他进程服务，不算本代理；
	// 通配监听（":8080"、"0.0.0.0:8080"、"[::]:8080"）时本机全部地址均属本代理
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsUnspecified() {
			g.boundIP = ip
		}
		return g
	}
	if host != "" {
		// 监听在主机名上：解析为具体地址；解析失败按通配处理（判定偏严，宁多判不漏判）
		if ips, lookupErr := net.LookupIP(host); lookupErr == nil && len(ips) > 0 {
			g.boundIP = ips[0]
		}
	}
	return g
}

// IsSelf 判断目标 host 是否指向本代理自身监听端口。
// host 为目标主机（可能不含端口），schema 用于推断缺省端口。
func (g *loopGuard) IsSelf(ctx context.Context, host, schema string) bool {
	if g == nil || g.port == "" || host == "" {
		return false
	}

	hostname, port := splitTargetHost(host, schema)
	if port != g.port {
		// 端口不同：本机其他端口由其他服务监听，直接放行（顺带避免无谓的域名解析）
		return false
	}

	return g.isLocalName(ctx, hostname)
}

// isLocalName 判断主机名是否指向本机地址
func (g *loopGuard) isLocalName(ctx context.Context, hostname string) bool {
	name := strings.ToLower(strings.Trim(hostname, "[]"))
	if name == "" {
		// 空主机名等价于通配地址，即本机
		return true
	}
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return true
	}
	if ip := net.ParseIP(name); ip != nil {
		return g.isLocalIP(ip)
	}

	// 域名：解析后比对本机地址；解析失败按非本机处理
	for _, ip := range g.lookup(ctx, name) {
		if g.isLocalIP(ip) {
			return true
		}
	}
	return false
}

// isLocalIP 判断地址是否属于本代理：
// 回环与未指定地址恒为本机；监听在具体地址时仅该地址；通配监听时为本机任一网卡地址
func (g *loopGuard) isLocalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	if g.boundIP != nil {
		return ip.Equal(g.boundIP)
	}
	return g.localAddrSet()[ip.String()]
}

// localAddrSet 返回本机网卡地址集合（带 TTL 缓存）
func (g *loopGuard) localAddrSet() map[string]bool {
	g.mu.RLock()
	addrs, at := g.localAddrs, g.localAt
	g.mu.RUnlock()
	if addrs != nil && time.Since(at) < loopAddrTTL {
		return addrs
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	// 双检：并发刷新时只保留先到者的结果
	if g.localAddrs != nil && time.Since(g.localAt) < loopAddrTTL {
		return g.localAddrs
	}

	fresh := make(map[string]bool)
	if list, err := net.InterfaceAddrs(); err == nil {
		for _, a := range list {
			if ipNet, ok := a.(*net.IPNet); ok {
				fresh[ipNet.IP.String()] = true
			}
		}
	}

	g.localAddrs = fresh
	g.localAt = time.Now()
	return fresh
}

// lookup 解析域名（带 TTL 缓存），失败返回 nil
func (g *loopGuard) lookup(ctx context.Context, host string) []net.IP {
	g.mu.RLock()
	entry, ok := g.dnsCache[host]
	g.mu.RUnlock()
	if ok && time.Since(entry.at) < loopDNSTTL {
		return entry.addrs
	}

	lookupCtx, cancel := context.WithTimeout(ctx, loopDNSTimeout)
	defer cancel()

	addrs, err := net.DefaultResolver.LookupIP(lookupCtx, "ip", host)
	if err != nil {
		addrs = nil
	}

	g.mu.Lock()
	if len(g.dnsCache) >= loopDNSCacheMax {
		g.dnsCache = make(map[string]loopDNSEntry)
	}
	g.dnsCache[host] = loopDNSEntry{addrs: addrs, at: time.Now()}
	g.mu.Unlock()

	return addrs
}

// splitTargetHost 拆分目标主机为 (hostname, port)；未携带端口时按 schema 取缺省端口
func splitTargetHost(host, schema string) (string, string) {
	h := strings.TrimSpace(host)
	if hostname, port, err := net.SplitHostPort(h); err == nil {
		if port != "" {
			return hostname, port
		}
		return hostname, defaultPortForSchema(schema)
	}
	// 未携带端口（含缺方括号的 IPv6 字面量）
	return strings.Trim(h, "[]"), defaultPortForSchema(schema)
}

// defaultPortForSchema 返回 scheme 对应的缺省端口
func defaultPortForSchema(schema string) string {
	switch strings.ToLower(schema) {
	case "https", "wss":
		return "443"
	default:
		return "80"
	}
}

// nextMaxForwards 计算转发给下游的剩余跳数；返回值 < 0 表示跳数已耗尽，不得继续转发。
// 客户端携带时按标准语义递减（但不得超过本代理上限，避免畸形值绕过跳数限制），
// 未携带或非数值时按本代理上限起算。
func nextMaxForwards(header string) int {
	remaining := maxForwardsLimit
	if s := strings.TrimSpace(header); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n < remaining {
			remaining = n
		}
	}
	return remaining - 1
}
