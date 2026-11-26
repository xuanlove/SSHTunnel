package config

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	// HostKey 主机密钥指纹/公钥（可选）。支持 SHA256:base64 指纹、base64 公钥
	// 或 authorized_keys 行；配置后严格校验服务器主机密钥以防中间人
	HostKey string `json:"host_key,omitempty"`
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

// migrateTLSFromFiles 仅迁移配置目录下 certs 内的有效证书对。
func migrateTLSFromFiles(listeners []ProxyListener, configDir string) {
	for i := range listeners {
		t := listeners[i].TLS
		if t == nil || t.CertFile == "" && t.KeyFile == "" {
			continue
		}
		certFile, keyFile := t.CertFile, t.KeyFile
		t.CertFile, t.KeyFile = "", ""
		if t.CertPEM != "" && t.KeyPEM != "" {
			continue
		}
		certDir := filepath.Join(configDir, "certs")
		info, err := os.Lstat(certDir)
		if err != nil || !info.IsDir() {
			continue
		}
		root, err := os.OpenRoot(certDir)
		if err != nil {
			continue
		}
		certPEM, keyPEM := t.CertPEM, t.KeyPEM
		if certPEM == "" {
			certPEM = readLegacyPEM(root, certDir, certFile)
		}
		if keyPEM == "" {
			keyPEM = readLegacyPEM(root, certDir, keyFile)
		}
		_ = root.Close()
		if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err == nil {
			t.CertPEM, t.KeyPEM = certPEM, keyPEM
		}
	}
}

func readLegacyPEM(root *os.Root, configDir, path string) string {
	if filepath.IsAbs(path) {
		relative, err := filepath.Rel(configDir, path)
		if err != nil {
			return ""
		}
		path = relative
	}
	if relative, ok := strings.CutPrefix(filepath.Clean(path), "certs"+string(filepath.Separator)); ok && !filepath.IsAbs(path) {
		path = relative
	}
	if !filepath.IsLocal(path) {
		return ""
	}
	info, err := root.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	f, err := root.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	const maxPEMSize = 4 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxPEMSize+1))
	if err != nil || len(data) > maxPEMSize {
		return ""
	}
	return string(data)
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

// Dir 返回应用配置目录（已创建，权限 0700）
func Dir() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}
	cfgDir := filepath.Join(dir, "sshtunnel")
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
		return "", err
	}
	return cfgDir, nil
}

// Manager 配置管理器
type Manager struct {
	mu      sync.RWMutex
	path    string
	configs map[string]*TunnelConfig
	// order 记录隧道的创建顺序，保证列表与持久化顺序稳定（map 遍历本身无序）。
	// 新建追加到末尾，删除移除；编辑不改变位置。
	order []string
}

// NewManager 创建配置管理器，配置文件保存到用户配置目录
func NewManager() (*Manager, error) {
	dir, err := userConfigDir()
	if err != nil {
		return nil, err
	}
	cfgDir := filepath.Join(dir, "sshtunnel")
	// 配置包含 SSH 密码与私钥，目录仅允许当前用户访问
	if err := os.MkdirAll(cfgDir, 0700); err != nil {
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
	m.order = m.order[:0]
	for i := range list {
		list[i].Status = StatusStopped
		// 兼容旧版：把 cert_file/key_file 文件内容读入 cert_pem/key_pem
		migrateTLSFromFiles(list[i].ProxyListeners, filepath.Dir(m.path))
		// 以配置文件中的数组顺序作为稳定顺序（重复 ID 以首个位置为准）
		if _, dup := m.configs[list[i].ID]; !dup {
			m.order = append(m.order, list[i].ID)
		}
		m.configs[list[i].ID] = &list[i]
	}
	return nil
}

// orderedLocked 按稳定顺序返回配置副本（调用方需持有读锁或写锁）。
// 顺序优先取创建顺序；历史数据若缺少顺序记录，按 ID 排序追加，保证每次输出一致。
func (m *Manager) orderedLocked() []TunnelConfig {
	out := make([]TunnelConfig, 0, len(m.configs))
	seen := make(map[string]bool, len(m.configs))
	for _, id := range m.order {
		if c, ok := m.configs[id]; ok && !seen[id] {
			out = append(out, *c)
			seen[id] = true
		}
	}
	if len(out) != len(m.configs) {
		missing := make([]string, 0, len(m.configs)-len(out))
		for id := range m.configs {
			if !seen[id] {
				missing = append(missing, id)
			}
		}
		sort.Strings(missing)
		for _, id := range missing {
			out = append(out, *m.configs[id])
		}
	}
	return out
}

// List 返回所有配置（按创建顺序）
func (m *Manager) List() []TunnelConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.orderedLocked()
}

// Get 获取单个配置副本（避免调用方持有内部指针导致并发读写）
func (m *Manager) Get(id string) (*TunnelConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.configs[id]
	if !ok {
		return nil, false
	}
	copied := *c
	return &copied, true
}

// Save 保存配置（新建或更新），ID 会回填到传入的 cfg
// ErrLegacyTLSPaths 表示写入请求仍携带弃用的本地证书路径。
var ErrLegacyTLSPaths = errors.New("HTTPS 代理证书必须以 PEM 内容提供")

func (m *Manager) Save(cfg *TunnelConfig) error {
	for _, listener := range cfg.ProxyListeners {
		if t := listener.TLS; t != nil && (t.CertFile != "" || t.KeyFile != "") {
			return ErrLegacyTLSPaths
		}
	}
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
	// 运行状态由 SetStatus 管理：保存配置不应把运行中的隧道覆盖为停止
	prev, exists := m.configs[cfg.ID]
	if exists && prev.Status != StatusStopped {
		copied.Status = prev.Status
	}
	m.configs[cfg.ID] = &copied
	if !exists {
		// 新建追加到末尾，保证列表位置稳定且符合创建顺序
		m.order = append(m.order, cfg.ID)
	}
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
	for i, oid := range m.order {
		if oid == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			break
		}
	}
	return m.saveLocked()
}

// saveLocked 持久化（调用方已持有写锁），按创建顺序写入
func (m *Manager) saveLocked() error {
	list := m.orderedLocked()
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	// 确保目录存在（防御性：目录可能在外部被删除）
	dir := filepath.Dir(m.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w (dir=%s)", err, dir)
	}
	// 原子写入：配置含明文凭据，使用 0600 临时文件 + rename，避免中断产生残缺文件
	tmp, err := os.CreateTemp(dir, ".configs-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时配置文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置配置权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步配置失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时配置文件失败: %w", err)
	}
	if err := os.Rename(tmpName, m.path); err != nil {
		return fmt.Errorf("替换配置文件失败: %w", err)
	}
	return nil
}

// SetStatus 更新运行时状态（不持久化）
func (m *Manager) SetStatus(id string, status TunnelStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.configs[id]; ok {
		c.Status = status
	}
}
