package proxy

import (
	_ "embed"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/oschwald/geoip2-golang"

	"sshtunnel/internal/config"
)

// builtinGeoDB 内置的 GeoLite2-Country 数据库。
// 数据来源：MaxMind GeoLite2 Country（CC BY-SA 4.0），见 geodata/README.md。
//
//go:embed geodata/GeoLite2-Country.mmdb
var builtinGeoDB []byte

// GeoResolver 将 IP 解析为国家代码（ISO 3166-1 alpha-2）。
// 抽象为接口便于测试替换。
type GeoResolver interface {
	// Country 返回国家代码；无法判定时返回空字符串与错误
	Country(ip net.IP) (string, error)
}

// geoipDB 全局 GeoIP 数据库（由 main 依据 --geoip-db 加载）
var (
	geoipMu sync.RWMutex
	geoipDB *geoip2.Reader
)

// SetGeoIPDatabase 加载外部 GeoLite2-Country 数据库（.mmdb），覆盖内置库。
// 传空字符串表示卸载（之后可再调用 LoadBuiltinGeoDatabase 恢复内置库）。
func SetGeoIPDatabase(path string) error {
	geoipMu.Lock()
	defer geoipMu.Unlock()

	if geoipDB != nil {
		_ = geoipDB.Close()
		geoipDB = nil
	}
	if path == "" {
		return nil
	}
	db, err := geoip2.Open(path)
	if err != nil {
		return fmt.Errorf("加载 GeoIP 数据库失败: %w", err)
	}
	geoipDB = db
	return nil
}

// LoadBuiltinGeoDatabase 从内嵌数据加载 GeoLite2-Country 数据库（无需外部文件）。
// 若已加载外部数据库则会被替换。
func LoadBuiltinGeoDatabase() error {
	if len(builtinGeoDB) == 0 {
		return fmt.Errorf("内置 GeoIP 数据库为空")
	}
	db, err := geoip2.FromBytes(builtinGeoDB)
	if err != nil {
		return fmt.Errorf("解析内置 GeoIP 数据库失败: %w", err)
	}
	geoipMu.Lock()
	if geoipDB != nil {
		_ = geoipDB.Close()
	}
	geoipDB = db
	geoipMu.Unlock()
	return nil
}

// GeoIPAvailable 当前是否已加载 GeoIP 数据库
func GeoIPAvailable() bool {
	geoipMu.RLock()
	defer geoipMu.RUnlock()
	return geoipDB != nil
}

// lookupCountry 使用全局数据库解析国家代码
func lookupCountry(ip net.IP) (string, error) {
	geoipMu.RLock()
	db := geoipDB
	geoipMu.RUnlock()
	if db == nil {
		return "", fmt.Errorf("未加载 GeoIP 数据库")
	}
	rec, err := db.Country(ip)
	if err != nil {
		return "", err
	}
	if rec == nil || rec.Country.IsoCode == "" {
		return "", nil
	}
	return strings.ToUpper(rec.Country.IsoCode), nil
}

// defaultGeoResolver 使用全局 GeoIP 数据库解析国家
type defaultGeoResolver struct{}

func (defaultGeoResolver) Country(ip net.IP) (string, error) { return lookupCountry(ip) }

// Firewall 代理监听器的简易防火墙
type Firewall struct {
	enabled   bool
	mode      config.FirewallMode
	nets      []*net.IPNet
	countries map[string]bool
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
	return len(f.nets) > 0 || len(f.countries) > 0
}

// Allow 判断给定 IP 是否允许通过。
//
// 语义：
//   - 未启用 → 放行
//   - 黑名单（默认）：命中 → 拒绝；未命中 → 放行
//   - 白名单：命中 → 放行；未命中 → 拒绝
//
// IP 规则与 GeoIP 国家规则取并集（任一命中即视为"命中名单"）。
// 解析失败 / 未启用 GeoIP 时按"未命中"处理，白名单模式下即拒绝（更安全）。
func (f *Firewall) Allow(ip net.IP) bool {
	if f == nil || !f.enabled {
		return true
	}
	if ip == nil {
		// 无法识别来源：白名单模式拒绝，黑名单模式放行
		return f.mode != config.FirewallWhitelist
	}

	matched := f.matchIP(ip)
	if !matched && len(f.countries) > 0 && f.geo != nil {
		if code, err := f.geo.Country(ip); err == nil && code != "" && f.countries[strings.ToUpper(code)] {
			matched = true
		}
	}

	if f.mode == config.FirewallWhitelist {
		return matched
	}
	return !matched
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
