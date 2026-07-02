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
	User        string `json:"user"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	AuthType    string `json:"auth_type"` // password | key
	Password    string `json:"password,omitempty"`
	KeyContent  string `json:"key_content,omitempty"`  // 密钥文本内容（PEM 格式）
	Passphrase  string `json:"passphrase,omitempty"`
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

// TLSConfig TLS 证书配置（HTTPS 代理专用）
type TLSConfig struct {
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

// ProxyListener 代理监听器
type ProxyListener struct {
	ID            string      `json:"id"`
	Protocol      ProxyProtocol `json:"protocol"` // http | https | socks4 | socks5
	ListenPort    int         `json:"listen_port"`
	AllowExternal bool        `json:"allow_external"`
	Auth          *AuthConfig `json:"auth,omitempty"`
	TLS           *TLSConfig  `json:"tls,omitempty"` // https 专用
}

// TunnelConfig 隧道完整配置
type TunnelConfig struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	HopChain      []HopConfig     `json:"hop_chain"`
	TunnelType    TunnelType      `json:"tunnel_type"`
	LocalForwards []LocalForward  `json:"local_forwards,omitempty"`
	ProxyListeners []ProxyListener `json:"proxy_listeners,omitempty"`
	AutoReconnect bool            `json:"auto_reconnect"`
	Status        TunnelStatus    `json:"status"` // 运行时状态
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
