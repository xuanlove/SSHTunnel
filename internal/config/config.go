package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/uuid"
)

// TunnelType 隧道类型
type TunnelType string

const (
	TunnelLocalForward TunnelType = "local_forward"
	TunnelProxy        TunnelType = "proxy"
)

// ProxyProtocol 代理协议
type ProxyProtocol string

const (
	ProxyHTTP   ProxyProtocol = "http"
	ProxyHTTPS  ProxyProtocol = "https"
	ProxySOCKS4 ProxyProtocol = "socks4"
	ProxySOCKS5 ProxyProtocol = "socks5"
)

// TunnelStatus 隧道运行状态
type TunnelStatus string

const (
	StatusStopped  TunnelStatus = "stopped"
	StatusStarting TunnelStatus = "starting"
	StatusRunning  TunnelStatus = "running"
	StatusError    TunnelStatus = "error"
)

// HopConfig 单跳 SSH 配置
type HopConfig struct {
	User       string `json:"user"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	AuthType   string `json:"auth_type"` // password | key
	Password   string `json:"password,omitempty"`
	KeyContent string `json:"key_content,omitempty"` // 密钥文本内容（PEM 格式）
	Passphrase string `json:"passphrase,omitempty"`
}

// LocalForward 本地端口转发
type LocalForward struct {
	ID            string `json:"id"`
	LocalPort     int    `json:"local_port"`
	RemoteHost    string `json:"remote_host"`
	RemotePort    int    `json:"remote_port"`
	AllowExternal bool   `json:"allow_external"`
}

// AuthConfig 认证配置
type AuthConfig struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// TLSConfig TLS 证书配置（HTTPS 代理专用）。
// 采用 PEM 文本方式存储，便于在界面中直接展示与编辑（certd 部署也写入此处）。
type TLSConfig struct {
	CertPEM string `json:"cert_pem,omitempty"` // 证书 PEM（可含完整证书链）
	KeyPEM  string `json:"key_pem,omitempty"`  // 私钥 PEM
	// CertFile / KeyFile 旧版文件路径字段：仅在加载旧配置时用于自动迁移为 PEM，之后不再写入
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
}

// migrateTLSFromFiles 将旧版文件路径配置迁移为 PEM 文本（读取成功后清空路径字段）
func migrateTLSFromFiles(listeners []ProxyListener) {
	for i := range listeners {
		t := listeners[i].TLS
		if t == nil {
			continue
		}
		if t.CertPEM == "" && t.CertFile != "" {
			if data, err := os.ReadFile(t.CertFile); err == nil {
				t.CertPEM = string(data)
			}
		}
		if t.KeyPEM == "" && t.KeyFile != "" {
			if data, err := os.ReadFile(t.KeyFile); err == nil {
				t.KeyPEM = string(data)
			}
		}
		if t.CertPEM != "" && t.KeyPEM != "" {
			t.CertFile, t.KeyFile = "", ""
		}
	}
}

// FirewallMode 防火墙模式
type FirewallMode string

const (
	// FirewallBlacklist 黑名单（默认）：命中名单的客户端被拒绝，其余放行
	FirewallBlacklist FirewallMode = "blacklist"
	// FirewallWhitelist 白名单：仅命中名单的客户端放行，其余拒绝
	FirewallWhitelist FirewallMode = "whitelist"
)

// FirewallConfig 代理监听器的简易防火墙配置
//
// 匹配规则（IP 与 GeoIP 国家取并集）：
//   - 黑名单模式（默认）：命中 → 拒绝；未命中 → 放行
//   - 白名单模式：命中 → 放行；未命中 → 拒绝
type FirewallConfig struct {
	Enabled bool         `json:"enabled"`
	Mode    FirewallMode `json:"mode"`          // blacklist(默认) | whitelist
	IPs     []string     `json:"ips,omitempty"` // 自定义 IP / CIDR，支持 IPv4 与 IPv6
	// Countries GeoIP 国家代码列表（ISO 3166-1 alpha-2，如 CN、US）。
	// 使用内置 GeoLite2-Country 数据库即可生效。
	Countries []string `json:"countries,omitempty"`
	// Cities 城市级规则，格式「国家/省/城市」（如 CN/GD/Shenzhen），各段支持 * 通配：
	//   CN/*/Shenzhen、CN/GD/*、*/GD/Shenzhen
	// 需要启用城市级数据库（Merged-IP，系统设置中下载并开启）才会生效。
	Cities []string `json:"cities,omitempty"`
}

// ProxyListener 代理监听器
type ProxyListener struct {
	ID            string        `json:"id"`
	Protocol      ProxyProtocol `json:"protocol"` // http | https | socks4 | socks5
	ListenPort    int           `json:"listen_port"`
	AllowExternal bool          `json:"allow_external"`
	Auth          *AuthConfig   `json:"auth,omitempty"`
	TLS           *TLSConfig    `json:"tls,omitempty"` // https 专用
	// Firewall 简易防火墙（懒启用；nil 或 Enabled=false 表示不限制）
	Firewall *FirewallConfig `json:"firewall,omitempty"`
	// AccessLog 是否记录代理访问日志（目标地址、耗时等），默认关闭避免日志刷屏
	AccessLog bool `json:"access_log,omitempty"`
}

// TunnelConfig 隧道完整配置
type TunnelConfig struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	HopChain       []HopConfig     `json:"hop_chain"`
	TunnelType     TunnelType      `json:"tunnel_type"`
	LocalForwards  []LocalForward  `json:"local_forwards,omitempty"`
	ProxyListeners []ProxyListener `json:"proxy_listeners,omitempty"`
	AutoReconnect  bool            `json:"auto_reconnect"`
	// AutoStart 程序/服务启动时自动启动该隧道（配合 systemd 服务可实现开机自启）
	AutoStart bool         `json:"auto_start"`
	Status    TunnelStatus `json:"status"` // 运行时状态
	// KeepAliveSeconds SSH 心跳间隔（秒），0 表示关闭；建议 60
	KeepAliveSeconds int `json:"keepalive_seconds,omitempty"`
	// MaxReconnectAttempts 最大重连次数，0 表示无限重试
	MaxReconnectAttempts int `json:"max_reconnect_attempts,omitempty"`
	// ReconnectMaxDelaySeconds 重连最大退避间隔（秒），0 表示默认 60
	ReconnectMaxDelaySeconds int `json:"reconnect_max_delay_seconds,omitempty"`
}

// userConfigDir 默认使用系统配置目录（可被测试覆盖）
var userConfigDir = os.UserConfigDir

// Manager 配置管理器
type Manager struct {
	mu      sync.RWMutex
	path    string
	configs map[string]*TunnelConfig
}

// NewManager 创建配置管理器，配置文件保存到用户配置目录
func NewManager() (*Manager, error) {
	dir, err := userConfigDir()
	if err != nil {
		return nil, err
	}
	cfgDir := filepath.Join(dir, "sshtunnel")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		return nil, err
	}
	return newManagerWithPath(filepath.Join(cfgDir, "configs.json"))
}

// newManagerWithPath 使用指定路径创建 Manager（供测试使用）
func newManagerWithPath(path string) (*Manager, error) {
	m := &Manager{
		path:    path,
		configs: make(map[string]*TunnelConfig),
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) load() error {
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var list []TunnelConfig
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range list {
		list[i].Status = StatusStopped
		// 兼容旧版：把 cert_file/key_file 文件内容读入 cert_pem/key_pem
		migrateTLSFromFiles(list[i].ProxyListeners)
		m.configs[list[i].ID] = &list[i]
	}
	return nil
}

// List 返回所有配置
func (m *Manager) List() []TunnelConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]TunnelConfig, 0, len(m.configs))
	for _, c := range m.configs {
		out = append(out, *c)
	}
	return out
}

// Get 获取单个配置
func (m *Manager) Get(id string) (*TunnelConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.configs[id]
	return c, ok
}

// Save 保存配置（新建或更新），ID 会回填到传入的 cfg
func (m *Manager) Save(cfg *TunnelConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cfg.ID == "" {
		cfg.ID = uuid.NewString()
	}
	if cfg.Status == "" {
		cfg.Status = StatusStopped
	}
	// 存储副本，避免外部修改影响内部状态
	copied := *cfg
	m.configs[cfg.ID] = &copied
	return m.saveLocked()
}

// Delete 删除配置
func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.configs[id]; !ok {
		return fmt.Errorf("配置不存在: %s", id)
	}
	delete(m.configs, id)
	return m.saveLocked()
}

// saveLocked 持久化（调用方已持有写锁）
func (m *Manager) saveLocked() error {
	list := make([]TunnelConfig, 0, len(m.configs))
	for _, c := range m.configs {
		list = append(list, *c)
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	// 确保目录存在（防御性：目录可能在外部被删除）
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建配置目录失败: %w (dir=%s)", err, dir)
	}
	return os.WriteFile(m.path, data, 0644)
}

// SetStatus 更新运行时状态（不持久化）
func (m *Manager) SetStatus(id string, status TunnelStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.configs[id]; ok {
		c.Status = status
	}
}
