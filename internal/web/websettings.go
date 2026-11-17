package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// WebTLSSettings WEB 面板 HTTPS 配置（持久化到 web_tls.json）
type WebTLSSettings struct {
	Enabled  bool   `json:"enabled"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
}

// DefaultWebTLSSettingsPath 返回默认持久化路径。
func DefaultWebTLSSettingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
			return ""
	}
	return filepath.Join(dir, "sshtunnel", "web_tls.json")
}

// LoadWebTLSSettings 读取 HTTPS 配置；文件不存在或解析失败时返回默认（未启用）。
func LoadWebTLSSettings(path string) WebTLSSettings {
	var s WebTLSSettings
	if path == "" {
		return s
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(raw, &s)
	return s
}

// SaveWebTLSSettings 持久化 HTTPS 配置。
func SaveWebTLSSettings(path string, s WebTLSSettings) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0600)
}

// TLSStore 运行时 WEB HTTPS 设置，支持在系统设置中动态开启/关闭。
type TLSStore struct {
	mu       sync.RWMutex
	path     string // 持久化路径；空字符串表示仅内存
	settings WebTLSSettings
	active   bool // 当前监听是否已启用 TLS
}

// NewTLSStore 创建 HTTPS 设置存储。path 非空时加载已保存配置。
func NewTLSStore(path string) *TLSStore {
	return &TLSStore{path: path, settings: LoadWebTLSSettings(path)}
}

// Settings 返回当前配置快照。
func (t *TLSStore) Settings() WebTLSSettings {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.settings
}

// Effective 返回启用状态下有效的证书与私钥路径。
func (t *TLSStore) Effective() (cert, key string, ok bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if !t.settings.Enabled || t.settings.CertFile == "" || t.settings.KeyFile == "" {
		return "", "", false
    }
	return t.settings.CertFile, t.settings.KeyFile, true
}

// Active 当前监听是否已启用 TLS。
func (t *TLSStore) Active() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.active
}

// SetActive 更新当前监听 TLS 状态（由 Server 调用）。
func (t *TLSStore) SetActive(active bool) {
	t.mu.Lock()
	t.active = active
	t.mu.Unlock()
}

// Save 更新并持久化配置。
func (t *TLSStore) Save(s WebTLSSettings) error {
	t.mu.Lock()
	t.settings = s
	path := t.path
	t.mu.Unlock()
	return SaveWebTLSSettings(path, s)
}