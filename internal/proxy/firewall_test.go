package proxy

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/port"
)

// stubGeo 测试用地理信息解析器：ip -> GeoInfo
type stubGeo map[string]*GeoInfo

func (s stubGeo) Lookup(ip net.IP) (*GeoInfo, error) {
	if g, ok := s[ip.String()]; ok {
		return g, nil
	}
	return nil, nil
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
		"1.2.3.4":      {Country: "CN", ProvinceCode: "BJ", Province: "Beijing", City: "Beijing"},
		"5.6.7.8":      {Country: "US", ProvinceCode: "CA", Province: "California", City: "San Francisco"},
		"2001:db8::aa": {Country: "JP", ProvinceCode: "13", Province: "Tokyo", City: "Tokyo"},
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

// TestFirewallCities 城市规则：国家/省/城市，支持 * 通配与省代码或英文名
func TestFirewallCities(t *testing.T) {
	geo := stubGeo{
		"1.1.1.1": {Country: "CN", ProvinceCode: "GD", Province: "Guangdong", City: "Shenzhen"},
		"2.2.2.2": {Country: "CN", ProvinceCode: "BJ", Province: "Beijing", City: "Beijing"},
		"3.3.3.3": {Country: "US", ProvinceCode: "CA", Province: "California", City: "San Francisco"},
		"4.4.4.4": {Country: "JP", ProvinceCode: "13", Province: "Tokyo", City: "Tokyo"},
	}

	cases := []struct {
		name  string
		rules []string
		ip    string
		want  bool // 黑名单模式下 Allow 结果
	}{
		{"精确城市-省代码", []string{"CN/GD/Shenzhen"}, "1.1.1.1", false},
		{"精确城市-省英文名", []string{"CN/Guangdong/Shenzhen"}, "1.1.1.1", false},
		{"大小写不敏感", []string{"cn/gd/shenzhen"}, "1.1.1.1", false},
		{"城市不匹配", []string{"CN/GD/Guangzhou"}, "1.1.1.1", true},
		{"国家通配", []string{"*/GD/Shenzhen"}, "1.1.1.1", false},
		{"省通配", []string{"CN/*/Shenzhen"}, "1.1.1.1", false},
		{"城市通配", []string{"CN/GD/*"}, "1.1.1.1", false},
		{"全省通配不匹配北京", []string{"CN/GD/*"}, "2.2.2.2", true},
		{"任意城市", []string{"CN/*/*"}, "2.2.2.2", false},
		{"多规则命中其一", []string{"US/CA/*", "JP/*/Tokyo"}, "4.4.4.4", false},
		{"多规则未命中", []string{"US/CA/*", "JP/*/Osaka"}, "4.4.4.4", true},
		{"仅国家段", []string{"US"}, "3.3.3.3", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fw := mustFW(t, &config.FirewallConfig{Enabled: true, Cities: c.rules}, geo)
			if got := fw.Allow(net.ParseIP(c.ip)); got != c.want {
				t.Errorf("Allow(%s) rules=%v = %v, want %v", c.ip, c.rules, got, c.want)
			}
		})
	}

	// 白名单：仅允许深圳
	fw := mustFW(t, &config.FirewallConfig{
		Enabled: true, Mode: config.FirewallWhitelist, Cities: []string{"CN/GD/Shenzhen"},
	}, geo)
	if !fw.Allow(net.ParseIP("1.1.1.1")) {
		t.Errorf("白名单命中深圳应放行")
	}
	if fw.Allow(net.ParseIP("2.2.2.2")) {
		t.Errorf("白名单未命中应拒绝")
	}
}

// TestFirewallCityRuleParseError 非法城市规则应报错
func TestFirewallCityRuleParseError(t *testing.T) {
	if _, err := NewFirewall(&config.FirewallConfig{
		Enabled: true, Cities: []string{"CN/GD"},
	}, nil); err == nil {
		t.Errorf("两段式城市规则应报错")
	}
}

// TestFirewallGeoIPWithoutDB 未加载数据库时国家/城市规则不命中
func TestFirewallGeoIPWithoutDB(t *testing.T) {
	fw := mustFW(t, &config.FirewallConfig{
		Enabled: true, Mode: config.FirewallWhitelist, Countries: []string{"CN"},
	}, defaultGeoResolver{})
	if fw.Allow(net.ParseIP("1.2.3.4")) {
		t.Errorf("无 GeoIP 数据库时白名单应拒绝")
	}
	fw2 := mustFW(t, &config.FirewallConfig{Enabled: true, Countries: []string{"CN"}}, defaultGeoResolver{})
	if !fw2.Allow(net.ParseIP("1.2.3.4")) {
		t.Errorf("无 GeoIP 数据库时黑名单应放行")
	}
	fw3 := mustFW(t, &config.FirewallConfig{Enabled: true, Cities: []string{"CN/GD/Shenzhen"}}, defaultGeoResolver{})
	if !fw3.Allow(net.ParseIP("1.2.3.4")) {
		t.Errorf("无数据库时黑名单城市规则应放行")
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

// TestBuiltinGeoDatabase 验证内置 GeoLite2-Country 可加载并解析 IPv4/IPv6
func TestBuiltinGeoDatabase(t *testing.T) {
	if err := LoadBuiltinGeoDatabase(); err != nil {
		t.Fatalf("加载内置 GeoIP 失败: %v", err)
	}
	defer unloadGeoDB()

	if !GeoIPAvailable() {
		t.Fatalf("加载后 GeoIPAvailable 应为 true")
	}
	if st := CurrentGeoIPStatus(); st.Type != GeoDBTypeCountry {
		t.Errorf("内置库类型应为 Country，实际 %s", st.Type)
	}
	for _, ip := range []string{"8.8.8.8", "114.114.114.114", "2001:4860:4860::8888"} {
		info, err := lookupGeo(net.ParseIP(ip))
		if err != nil {
			t.Errorf("解析 %s 失败: %v", ip, err)
			continue
		}
		if info == nil || info.Country == "" {
			t.Errorf("%s 未解析出国家代码", ip)
		}
	}
	if info, err := lookupGeo(net.ParseIP("192.168.1.1")); err != nil {
		t.Errorf("私有地址解析不应报错: %v", err)
	} else if info != nil && info.Country != "" {
		t.Errorf("私有地址不应有国家代码，实际 %q", info.Country)
	}
}

// TestBuiltinGeoFirewall 内置 Country 库驱动国家规则
func TestBuiltinGeoFirewall(t *testing.T) {
	if err := LoadBuiltinGeoDatabase(); err != nil {
		t.Fatalf("加载内置 GeoIP 失败: %v", err)
	}
	defer unloadGeoDB()

	ip := net.ParseIP("114.114.114.114")
	info, err := lookupGeo(ip)
	if err != nil || info == nil || info.Country == "" {
		t.Skipf("数据库未覆盖该 IP，跳过: %v", err)
	}
	fw := mustFW(t, &config.FirewallConfig{Enabled: true, Countries: []string{info.Country}}, defaultGeoResolver{})
	if fw.Allow(ip) {
		t.Errorf("黑名单国家 %s 应拒绝 %s", info.Country, ip)
	}
	fw2 := mustFW(t, &config.FirewallConfig{
		Enabled: true, Mode: config.FirewallWhitelist, Countries: []string{info.Country},
	}, defaultGeoResolver{})
	if !fw2.Allow(ip) {
		t.Errorf("白名单国家 %s 应放行 %s", info.Country, ip)
	}
}

// TestGeoIPSettingsPersistence 验证设置持久化
func TestGeoIPSettingsPersistence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if s := LoadGeoIPSettings(); s.Enabled {
		t.Errorf("默认应为未启用")
	}
	if err := SaveGeoIPSettings(GeoIPSettings{Enabled: true}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if s := LoadGeoIPSettings(); !s.Enabled {
		t.Errorf("重新加载后应保持启用")
	}
	if got := CityDatabasePath(); got != filepath.Join(dir, "sshtunnel", "Merged-IP.mmdb") {
		t.Errorf("城市库缓存路径不符: %s", got)
	}
}

// TestFirewallBlockLogThrottle 同一来源 IP 的拦截日志十分钟内只记录一次
func TestFirewallBlockLogThrottle(t *testing.T) {
	var mu sync.Mutex
	var warns []string
	b := &BaseServer{
		fw: mustFW(t, &config.FirewallConfig{Enabled: true, IPs: []string{"203.0.113.0/24"}}, nil),
		log: func(level, msg string) {
			if level != "warn" {
				return
			}
			mu.Lock()
			warns = append(warns, msg)
			mu.Unlock()
		},
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(warns)
	}
	connFrom := func(ip string) net.Conn {
		return fakeConn{addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 5000}}
	}

	// 同一 IP 连续被拒 5 次，只记录 1 条
	for i := 0; i < 5; i++ {
		if b.allowConn(connFrom("203.0.113.5")) {
			t.Fatalf("黑名单命中的来源应被拒绝")
		}
	}
	if got := count(); got != 1 {
		t.Fatalf("同一 IP 应只记录 1 条拦截日志，实际 %d", got)
	}

	// 另一个被拒 IP 应单独记录
	b.allowConn(connFrom("203.0.113.6"))
	if got := count(); got != 2 {
		t.Errorf("不同 IP 应各自记录，实际 %d 条", got)
	}

	// 放行的 IP 不记录
	b.allowConn(connFrom("198.51.100.1"))
	if got := count(); got != 2 {
		t.Errorf("放行来源不应记录日志，实际 %d 条", got)
	}

	// 超过十分钟窗口后应再次记录
	b.blockLogMu.Lock()
	b.blockLogAt["203.0.113.5"] = time.Now().Add(-firewallBlockLogInterval - time.Minute)
	b.blockLogMu.Unlock()
	b.allowConn(connFrom("203.0.113.5"))
	if got := count(); got != 3 {
		t.Errorf("窗口期后应再次记录，实际 %d 条", got)
	}
}

// fakeConn 仅提供 RemoteAddr 的测试连接
type fakeConn struct {
	net.Conn
	addr net.Addr
}

func (f fakeConn) RemoteAddr() net.Addr { return f.addr }

// unloadGeoDB 测试辅助：卸载全局数据库，避免用例间相互影响
func unloadGeoDB() {
	geoipMu.Lock()
	if geoipDB != nil {
		_ = geoipDB.Close()
		geoipDB = nil
	}
	geoipSource, geoipType, geoipSize = "", "", 0
	geoipMu.Unlock()
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
