package web

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"sshtunnel/internal/persist"
)

// WebTLSSettings WEB 面板 HTTPS 与 certd 部署配置（持久化到 web_tls.json）
type WebTLSSettings struct {
	Enabled bool `json:"enabled"`
	// UseIPCert 启用 HTTPS 时使用「系统设置 → IP 证书」的证书：
	// 无需填写证书内容，前提是 IP 证书已启用且完成签发；续期后动态生效
	UseIPCert bool   `json:"use_ipcert,omitempty"`
	CertFile  string `json:"cert_file"`
	KeyFile   string `json:"key_file"`
	// DeployToken certd 等外部工具调用证书部署 API 所需的令牌
	DeployToken string `json:"deploy_token,omitempty"`
}

// DefaultWebTLSSettingsPath 返回默认持久化路径。
func DefaultWebTLSSettingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "sshtunnel", "web_tls.json")
}

// WebCertDir 返回 WEB 面板证书存放目录。
func WebCertDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "sshtunnel", "certs")
}

func loadWebTLSSettings(path string) (WebTLSSettings, error) {
	var s WebTLSSettings
	if path == "" {
		return s, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	stored := struct {
		*WebTLSSettings
		Enabled *bool `json:"enabled"`
	}{WebTLSSettings: &s}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return s, err
	}
	if stored.Enabled == nil {
		return s, errors.New("缺少 WEB HTTPS 开关")
	}
	s.Enabled = *stored.Enabled
	return s, nil
}

// SaveWebTLSSettings 持久化配置。
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
	return persist.WriteFileAtomic(path, raw)
}

// TLSStore 运行时 WEB HTTPS 设置，支持在系统设置中动态开启/关闭。
type TLSStore struct {
	mu       sync.RWMutex
	path     string // 持久化路径；空字符串表示仅内存
	settings WebTLSSettings
	loadErr  error
	active   bool // 当前监听是否已启用 TLS
	// ipcert IP 证书动态来源（UseIPCert 时使用）；续期后无需重启监听
	ipcert func() (tls.Certificate, error)
}

// NewTLSStore 创建 HTTPS 设置存储。path 非空时加载已保存配置。
func NewTLSStore(path string) *TLSStore {
	settings, err := loadWebTLSSettings(path)
	if err != nil {
		err = fmt.Errorf("加载 WEB HTTPS 设置失败: %w", err)
	}
	return &TLSStore{path: path, settings: settings, loadErr: err}
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

// SetIPCertProvider 注入 IP 证书动态来源（由 Handler.SetIPCert 调用）。
func (t *TLSStore) SetIPCertProvider(p func() (tls.Certificate, error)) {
	t.mu.Lock()
	t.ipcert = p
	t.mu.Unlock()
}

func (t *TLSStore) listenerConfig() (*tls.Config, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.loadErr != nil {
		return nil, t.loadErr
	}
	if !t.settings.Enabled {
		return nil, nil
	}
	// 使用 IP 证书：动态获取，续期后无需重启监听
	if t.settings.UseIPCert {
		if t.ipcert == nil {
			return nil, errors.New("IP 证书功能不可用")
		}
		return &tls.Config{
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				cert, err := t.ipcert()
				if err != nil {
					return nil, fmt.Errorf("IP 证书不可用: %w", err)
				}
				return &cert, nil
			},
			MinVersion: tls.VersionTLS12,
		}, nil
	}
	cert, err := tls.LoadX509KeyPair(t.settings.CertFile, t.settings.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("WEB 面板 HTTPS 证书加载失败: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
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

// Save 更新并持久化配置（保留已生成的部署 Token）。
func (t *TLSStore) Save(s WebTLSSettings) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s.Enabled && !s.UseIPCert {
		if _, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile); err != nil {
			return err
		}
	}
	return t.saveLocked(s)
}

func (t *TLSStore) saveLocked(s WebTLSSettings) error {
	if s.DeployToken == "" {
		s.DeployToken = t.settings.DeployToken
	}
	if err := SaveWebTLSSettings(t.path, s); err != nil {
		return err
	}
	old := t.settings
	t.settings = s
	t.loadErr = nil
	if old.CertFile != s.CertFile && old.KeyFile != s.KeyFile {
		removeManagedPair(old)
	}
	return nil
}

// SavePEM 先完整写入新证书对，再原子切换设置文件。
func (t *TLSStore) SavePEM(enabled bool, certPEM, keyPEM string) (WebTLSSettings, error) {
	if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
		return WebTLSSettings{}, fmt.Errorf("证书或私钥内容无效: %w", err)
	}
	base := WebCertDir()
	if base == "" {
		return WebTLSSettings{}, os.ErrInvalid
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return WebTLSSettings{}, err
	}
	dir, err := os.MkdirTemp(base, "pair-")
	if err != nil {
		return WebTLSSettings{}, err
	}
	s := WebTLSSettings{Enabled: enabled, CertFile: filepath.Join(dir, "web.crt"), KeyFile: filepath.Join(dir, "web.key")}
	defer func() {
		if err != nil {
			removeManagedPair(s)
		}
	}()
	if err = persist.WriteFileAtomic(s.CertFile, []byte(certPEM)); err != nil {
		return WebTLSSettings{}, err
	}
	if err = persist.WriteFileAtomic(s.KeyFile, []byte(keyPEM)); err != nil {
		return WebTLSSettings{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	err = t.saveLocked(s)
	return s, err
}

func removeManagedPair(s WebTLSSettings) {
	dir := filepath.Dir(s.CertFile)
	if filepath.Dir(dir) == WebCertDir() && strings.HasPrefix(filepath.Base(dir), "pair-") &&
		s.CertFile == filepath.Join(dir, "web.crt") && s.KeyFile == filepath.Join(dir, "web.key") {
		_ = os.Remove(s.CertFile)
		_ = os.Remove(s.KeyFile)
		_ = os.Remove(dir)
	}
}

// DeployToken 返回 certd 部署令牌（可能为空）。
func (t *TLSStore) DeployToken() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.settings.DeployToken
}

// EnsureDeployToken 确保部署令牌已生成并持久化。
func (t *TLSStore) EnsureDeployToken() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.settings.DeployToken == "" {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return ""
		}
		next := t.settings
		next.DeployToken = hex.EncodeToString(b)
		if err := t.saveLocked(next); err != nil {
			return ""
		}
	}
	return t.settings.DeployToken
}

// RegenerateDeployToken 重新生成部署令牌并使旧令牌立即失效。
func (t *TLSStore) RegenerateDeployToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	t.mu.Lock()
	defer t.mu.Unlock()
	next := t.settings
	next.DeployToken = token
	if err := t.saveLocked(next); err != nil {
		return "", err
	}
	return token, nil
}

// EnableWithFiles 使用指定证书文件启用 HTTPS（--tls-cert/--tls-key 启动参数）。
func (t *TLSStore) EnableWithFiles(certFile, keyFile string) error {
	return t.Save(WebTLSSettings{
		Enabled:  true,
		CertFile: certFile,
		KeyFile:  keyFile,
	})
}
