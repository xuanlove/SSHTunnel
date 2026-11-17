package proxy

import (
	_ "embed"
	"fmt"
	"net"
	"strings"

	"sshtunnel/internal/config"
)

// builtinGeoDB 内置的 GeoLite2-Country 数据库。
// 数据来源：MaxMind GeoLite2 Country（CC BY-SA 4.0），见 geodata/README.md。
// 城市级数据（GeoLite2-City）体积较大，改为在系统设置中按需下载。
//
//go:embed geodata/GeoLite2-Country.mmdb
var builtinGeoDB []byte

// SetGeoIPDatabase 兼容入口：加载外部 mmdb（空字符串表示卸载）
func SetGeoIPDatabase(path string) error {
	if path == "" {
		geoipMu.Lock()
		old := geoipDB
		geoipDB = nil
		geoipSource = ""
		geoipType = ""
		geoipSize = 0
		geoipMu.Unlock()
		if old != nil {
			_ = old.Close()
		}
		return nil
	}
	return LoadGeoIPDatabaseFile(path)
}

// cityRule 城市规则：国家/省/城市，各段支持 * 通配
type cityRule struct {
	country  string
	province string
	city     string
}

// parseCityRule 解析 "国家/省/城市" 规则
func parseCityRule(raw string) (cityRule, error) {
	parts := strings.Split(strings.TrimSpace(raw), "/")
	if len(parts) == 1 {
		// 仅给出国家：等价于国家规则
		return cityRule{country: strings.ToLower(parts[0]), province: "*", city: "*"}, nil
	}
	if len(parts) != 3 {
		return cityRule{}, fmt.Errorf("格式应为 国家/省/城市（如 CN/GD/Shenzhen），当前: %q", raw)
	}
	rule := cityRule{
		country:  strings.ToLower(strings.TrimSpace(parts[0])),
		province: strings.ToLower(strings.TrimSpace(parts[1])),
		city:     strings.ToLower(strings.TrimSpace(parts[2])),
	}
	if rule.country == "" {
		rule.country = "*"
	}
	if rule.province == "" {
		rule.province = "*"
	}
	if rule.city == "" {
		rule.city = "*"
	}
	return rule, nil
}

// Firewall 代理监听器的简易防火墙
type Firewall struct {
	enabled   bool
	mode      config.FirewallMode
	nets      []*net.IPNet
	countries map[string]bool
	cities    []cityRule
	geo       GeoResolver
}

// NewFirewall 根据配置构建防火墙。
// cfg 为 nil 或未启用时返回一个全部放行的实例（永不拒绝）。
func NewFirewall(cfg *config.FirewallConfig, geo GeoResolver) (*Firewall, error) {
	fw := &Firewall{geo: geo}
	if cfg == nil || !cfg.Enabled {
		return fw, nil
	}
	fw.enabled = true
	fw.mode = cfg.Mode
	if fw.mode == "" {
		fw.mode = config.FirewallBlacklist
	}
	if fw.mode != config.FirewallBlacklist && fw.mode != config.FirewallWhitelist {
		return nil, fmt.Errorf("无效的防火墙模式: %s", cfg.Mode)
	}

	for _, raw := range cfg.IPs {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		ipNet, err := parseIPOrCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("无效的 IP/CIDR %q: %w", entry, err)
		}
		fw.nets = append(fw.nets, ipNet)
	}

	if len(cfg.Countries) > 0 {
		fw.countries = make(map[string]bool, len(cfg.Countries))
		for _, c := range cfg.Countries {
			code := strings.ToUpper(strings.TrimSpace(c))
			if code != "" {
				fw.countries[code] = true
			}
		}
	}

	for _, raw := range cfg.Cities {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		rule, err := parseCityRule(entry)
		if err != nil {
			return nil, fmt.Errorf("无效的城市规则 %q: %w", entry, err)
		}
		fw.cities = append(fw.cities, rule)
	}
	return fw, nil
}

// parseIPOrCIDR 解析单个 IP 或 CIDR（支持 IPv4 / IPv6）
func parseIPOrCIDR(s string) (*net.IPNet, error) {
	if strings.Contains(s, "/") {
		_, ipNet, err := net.ParseCIDR(s)
		if err != nil {
			return nil, err
		}
		return ipNet, nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil, fmt.Errorf("无法解析 IP 地址")
	}
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)}, nil
}

// Enforced 是否实际生效（启用且有匹配条件）
func (f *Firewall) Enforced() bool {
	if f == nil || !f.enabled {
		return false
	}
	return len(f.nets) > 0 || len(f.countries) > 0 || len(f.cities) > 0
}

// Allow 判断给定 IP 是否允许通过。
//
// 语义：
//   - 未启用 → 放行
//   - 黑名单（默认）：命中 → 拒绝；未命中 → 放行
//   - 白名单：命中 → 放行；未命中 → 拒绝
//
// IP 规则、GeoIP 国家规则与城市规则取并集（任一命中即视为"命中名单"）。
// 解析失败 / 未加载 GeoIP 时按"未命中"处理，白名单模式下即拒绝（更安全）。
func (f *Firewall) Allow(ip net.IP) bool {
	if f == nil || !f.enabled {
		return true
	}
	if ip == nil {
		// 无法识别来源：白名单模式拒绝，黑名单模式放行
		return f.mode != config.FirewallWhitelist
	}

	matched := f.matchIP(ip)
	if !matched && f.needsGeo() && f.geo != nil {
		if info, err := f.geo.Lookup(ip); err == nil && info != nil {
			matched = f.matchGeo(info)
		}
	}

	if f.mode == config.FirewallWhitelist {
		return matched
	}
	return !matched
}

// needsGeo 是否需要查询 GeoIP（配置了国家或城市规则）
func (f *Firewall) needsGeo() bool {
	return len(f.countries) > 0 || len(f.cities) > 0
}

// matchGeo 判断地理位置是否命中名单
func (f *Firewall) matchGeo(info *GeoInfo) bool {
	if info == nil {
		return false
	}
	if len(f.countries) > 0 && info.Country != "" && f.countries[strings.ToUpper(info.Country)] {
		return true
	}
	for _, rule := range f.cities {
		if rule.match(info) {
			return true
		}
	}
	return false
}

// match 城市规则匹配：国家代码、省（代码或英文名）、城市英文名，均大小写不敏感
func (r cityRule) match(info *GeoInfo) bool {
	if !segmentMatch(r.country, info.Country) {
		return false
	}
	// 省支持 ISO 代码（GD）或英文名（Guangdong）
	if !segmentMatch(r.province, info.ProvinceCode) && !segmentMatch(r.province, info.Province) {
		return false
	}
	return segmentMatch(r.city, info.City)
}

// segmentMatch 单段匹配：* 表示通配；否则大小写不敏感相等
func segmentMatch(pattern, value string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	if value == "" {
		return false
	}
	return strings.EqualFold(pattern, value)
}

// matchIP 判断 IP 是否命中自定义 IP/CIDR 列表
func (f *Firewall) matchIP(ip net.IP) bool {
	for _, n := range f.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// remoteIP 提取连接的远端 IP
func remoteIP(conn net.Conn) net.IP {
	if conn == nil || conn.RemoteAddr() == nil {
		return nil
	}
	switch addr := conn.RemoteAddr().(type) {
	case *net.TCPAddr:
		return addr.IP
	default:
		host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
		if err != nil {
			return nil
		}
		return net.ParseIP(host)
	}
}
