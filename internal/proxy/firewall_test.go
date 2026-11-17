package proxy

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/port"
)

// stubGeo 测试用国家解析器
type stubGeo map[string]string // ip -> country

func (s stubGeo) Country(ip net.IP) (string, error) {
	if c, ok := s[ip.String()]; ok {
		return c, nil
	}
	return "", nil
}

func mustFW(t *testing.T, cfg *config.FirewallConfig, geo GeoResolver) *Firewall {
	t.Helper()
	fw, err := NewFirewall(cfg, geo)
	if err != nil {
		t.Fatalf("构建防火墙失败: %v", err)
	}
	return fw
}

// TestFirewallDisabled 未启用时全部放行
func TestFirewallDisabled(t *testing.T) {
	for _, cfg := range []*config.FirewallConfig{nil, {Enabled: false, Mode: config.FirewallWhitelist, IPs: []string{"10.0.0.0/8"}}} {
		fw := mustFW(t, cfg, nil)
		if !fw.Allow(net.ParseIP("10.1.2.3")) {
			t.Errorf("未启用时不应拒绝任何来源")
		}
		if fw.Enforced() {
			t.Errorf("未启用时 Enforced 应为 false")
		}
	}
}

// TestFirewallBlacklist 黑名单（默认）：命中拒绝，其余放行
func TestFirewallBlacklist(t *testing.T) {
	fw := mustFW(t, &config.FirewallConfig{
		Enabled: true,
		IPs:     []string{"203.0.113.0/24", "2001:db8::/32", "198.51.100.7"},
	}, nil)

	cases := []struct {
		ip   string
		want bool
	}{
		{"203.0.113.9", false},   // IPv4 CIDR 命中
		{"203.0.113.255", false}, // 边界命中
		{"203.0.114.1", true},    // 不在名单
		{"198.51.100.7", false},  // 单 IP 命中
		{"198.51.100.8", true},   // 相邻 IP 放行
		{"2001:db8::1", false},   // IPv6 CIDR 命中
		{"2001:db9::1", true},    // 其他 IPv6 放行
		{"8.8.8.8", true},
	}
	for _, c := range cases {
		if got := fw.Allow(net.ParseIP(c.ip)); got != c.want {
			t.Errorf("黑名单 Allow(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// TestFirewallWhitelist 白名单：命中放行，其余拒绝
func TestFirewallWhitelist(t *testing.T) {
	fw := mustFW(t, &config.FirewallConfig{
		Enabled: true,
		Mode:    config.FirewallWhitelist,
		IPs:     []string{"192.168.1.0/24", "fd00::/8"},
	}, nil)

	cases := []struct {
		ip   string
		want bool
	}{
		{"192.168.1.10", true},
		{"192.168.2.10", false},
		{"fd00::abcd", true},
		{"fe80::1", false},
	}
	for _, c := range cases {
		if got := fw.Allow(net.ParseIP(c.ip)); got != c.want {
			t.Errorf("白名单 Allow(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// TestFirewallIPv6Single 单个 IPv6 地址（/128）匹配
func TestFirewallIPv6Single(t *testing.T) {
	fw := mustFW(t, &config.FirewallConfig{Enabled: true, IPs: []string{"2001:db8::dead:beef"}}, nil)
	if fw.Allow(net.ParseIP("2001:db8::dead:beef")) {
		t.Errorf("黑名单命中的单个 IPv6 应被拒绝")
	}
	if !fw.Allow(net.ParseIP("2001:db8::dead:bee0")) {
		t.Errorf("未命中的 IPv6 应放行")
	}
}

// TestFirewallGeoIP 国家规则（IP 与国家取并集）
func TestFirewallGeoIP(t *testing.T) {
	geo := stubGeo{
		"1.2.3.4":      "CN",
		"5.6.7.8":      "US",
		"2001:db8::aa": "JP",
	}
	// 黑名单：拒绝 CN
	fw := mustFW(t, &config.FirewallConfig{Enabled: true, Countries: []string{"cn"}}, geo)
	if fw.Allow(net.ParseIP("1.2.3.4")) {
		t.Errorf("黑名单国家 CN 应被拒绝")
	}
	if !fw.Allow(net.ParseIP("5.6.7.8")) {
		t.Errorf("非名单国家 US 应放行")
	}

	// 白名单：仅允许 JP（含 IPv6）
	fw2 := mustFW(t, &config.FirewallConfig{
		Enabled: true, Mode: config.FirewallWhitelist, Countries: []string{"JP"},
	}, geo)
	if !fw2.Allow(net.ParseIP("2001:db8::aa")) {
		t.Errorf("白名单国家 JP（IPv6）应放行")
	}
	if fw2.Allow(net.ParseIP("5.6.7.8")) {
		t.Errorf("白名单未命中应拒绝")
	}
}

// TestFirewallGeoIPWithoutDB 未配置 GeoIP 时国家规则不命中（白名单下拒绝）
func TestFirewallGeoIPWithoutDB(t *testing.T) {
	fw := mustFW(t, &config.FirewallConfig{
		Enabled: true, Mode: config.FirewallWhitelist, Countries: []string{"CN"},
	}, defaultGeoResolver{})
	// 未加载数据库：无法判定 → 白名单拒绝
	if fw.Allow(net.ParseIP("1.2.3.4")) {
		t.Errorf("无 GeoIP 数据库时白名单应拒绝")
	}

	// 黑名单模式下无数据库 → 放行
	fw2 := mustFW(t, &config.FirewallConfig{Enabled: true, Countries: []string{"CN"}}, defaultGeoResolver{})
	if !fw2.Allow(net.ParseIP("1.2.3.4")) {
		t.Errorf("无 GeoIP 数据库时黑名单应放行")
	}
}

// TestFirewallInvalidConfig 非法配置应报错
func TestFirewallInvalidConfig(t *testing.T) {
	if _, err := NewFirewall(&config.FirewallConfig{Enabled: true, Mode: "bogus"}, nil); err == nil {
		t.Errorf("非法模式应报错")
	}
	if _, err := NewFirewall(&config.FirewallConfig{Enabled: true, IPs: []string{"not-an-ip"}}, nil); err == nil {
		t.Errorf("非法 IP 应报错")
	}
	if _, err := NewFirewall(&config.FirewallConfig{Enabled: true, IPs: []string{"10.0.0.0/99"}}, nil); err == nil {
		t.Errorf("非法 CIDR 应报错")
	}
}

// TestFirewallNilIP 无法识别来源时的保守策略
func TestFirewallNilIP(t *testing.T) {
	bl := mustFW(t, &config.FirewallConfig{Enabled: true, IPs: []string{"10.0.0.0/8"}}, nil)
	if !bl.Allow(nil) {
		t.Errorf("黑名单模式下未知来源应放行")
	}
	wl := mustFW(t, &config.FirewallConfig{Enabled: true, Mode: config.FirewallWhitelist, IPs: []string{"10.0.0.0/8"}}, nil)
	if wl.Allow(nil) {
		t.Errorf("白名单模式下未知来源应拒绝")
	}
}

// TestBuildServerWithFirewall 验证 BuildServer 正确装配防火墙
func TestBuildServerWithFirewall(t *testing.T) {
	_, err := BuildServer(config.ProxyListener{
		ID: "fw-test", Protocol: config.ProxySOCKS5, ListenPort: 0,
		Firewall: &config.FirewallConfig{Enabled: true, IPs: []string{"bad-cidr"}},
	}, &mockDialer{}, nil)
	if err == nil {
		t.Errorf("非法防火墙配置应导致 BuildServer 失败")
	}

	srv, err := BuildServer(config.ProxyListener{
		ID: "fw-ok", Protocol: config.ProxySOCKS5, ListenPort: 0,
		Firewall: &config.FirewallConfig{Enabled: true, IPs: []string{"203.0.113.0/24"}},
	}, &mockDialer{}, nil)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if srv == nil {
		t.Fatalf("期望非空服务器")
	}
}

// TestFirewallEnforcedOnListener 端到端验证：真实监听器上防火墙实际拦截连接
func TestFirewallEnforcedOnListener(t *testing.T) {
	target, targetAddr := startTargetServer(t, "203.0.113.9\n")
	defer target.Close()

	start := func(fwCfg *config.FirewallConfig) (int, func()) {
		p := port.FindAvailable(26000)
		if p == 0 {
			t.Fatalf("无法找到空闲端口")
		}
		srv, err := BuildServer(config.ProxyListener{
			ID:         "fw-e2e",
			Protocol:   config.ProxySOCKS5,
			ListenPort: p,
			Firewall:   fwCfg,
		}, &mockDialer{target: targetAddr}, nil)
		if err != nil {
			t.Fatalf("构建失败: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		go srv.Start(ctx)
		addr := "127.0.0.1:" + strconv.Itoa(p)
		deadline := time.Now().Add(3 * time.Second)
		for {
			c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
			if err == nil {
				c.Close()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("监听器未就绪: %v", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		return p, func() { cancel(); srv.Stop() }
	}

	targetURL := "http://" + targetAddr + "/ip"

	// 1) 黑名单拒绝本机 → 探测失败
	p1, stop1 := start(&config.FirewallConfig{Enabled: true, IPs: []string{"127.0.0.1"}})
	defer stop1()
	if _, err := Probe(ProbeOptions{Protocol: "socks5", Host: "127.0.0.1", Port: p1, Target: targetURL}); err == nil {
		t.Errorf("黑名单命中本机时应拒绝连接")
	}

	// 2) 白名单允许本机 → 探测成功
	p2, stop2 := start(&config.FirewallConfig{
		Enabled: true, Mode: config.FirewallWhitelist, IPs: []string{"127.0.0.0/8"},
	})
	defer stop2()
	res, err := Probe(ProbeOptions{Protocol: "socks5", Host: "127.0.0.1", Port: p2, Target: targetURL})
	if err != nil {
		t.Fatalf("白名单允许本机时应可连接: %v", err)
	}
	if res.IP != "203.0.113.9" {
		t.Errorf("出口 IP = %q, want 203.0.113.9", res.IP)
	}

	// 3) 白名单未命中本机 → 拒绝
	p3, stop3 := start(&config.FirewallConfig{
		Enabled: true, Mode: config.FirewallWhitelist, IPs: []string{"fd00::/8"},
	})
	defer stop3()
	if _, err := Probe(ProbeOptions{Protocol: "socks5", Host: "127.0.0.1", Port: p3, Target: targetURL}); err == nil {
		t.Errorf("白名单未命中时应拒绝连接")
	}

	// 4) 未启用防火墙 → 放行
	p4, stop4 := start(nil)
	defer stop4()
	if _, err := Probe(ProbeOptions{Protocol: "socks5", Host: "127.0.0.1", Port: p4, Target: targetURL}); err != nil {
		t.Errorf("未启用防火墙时应放行: %v", err)
	}
}
