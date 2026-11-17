package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// WebTLSSettings WEB 面板 HTTPS 与 certd 部署配置（持久化到 web_tls.json）
type WebTLSSettings struct {
	Enabled  bool   `json:"enabled"`
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
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

// WebCertPath WEB 面板证书文件路径（用户填写证书内容时写入此处）。
func WebCertPath() string {
	dir := WebCertDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "web.crt")
}

// WebKeyPath WEB 面板私钥文件路径。
func WebKeyPath() string {
	dir := WebCertDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "web.key")
}

// LoadWebTLSSettings 读取配置；文件不存在或解析失败时返回默认（未启用）。
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
	return os.WriteFile(path, raw, 0600)
}

// WritePEMFile 原子写入 PEM 文件（先写临时文件再 rename）。
func WritePEMFile(path string, data []byte, mode os.FileMode) error {
	if path == "" {
		return os.ErrInvalid
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".pem-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
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

// Save 更新并持久化配置（保留已生成的部署 Token）。
func (t *TLSStore) Save(s WebTLSSettings) error {
	t.mu.Lock()
	if s.DeployToken == "" {
		s.DeployToken = t.settings.DeployToken
	}
	t.settings = s
	path := t.path
	t.mu.Unlock()
	return SaveWebTLSSettings(path, s)
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
		t.settings.DeployToken = hex.EncodeToString(b)
		if t.path != "" {
			_ = SaveWebTLSSettings(t.path, t.settings)
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
	t.settings.DeployToken = token
	path := t.path
	s := t.settings
	t.mu.Unlock()
	if path != "" {
		if err := SaveWebTLSSettings(path, s); err != nil {
			return "", err
		}
	}
	return token, nil
}

// EnableWithFiles 使用指定证书文件启用 HTTPS（--tls-cert/--tls-key 启动参数）。
func (t *TLSStore) EnableWithFiles(certFile, keyFile string) error {
	return t.Save(WebTLSSettings{
		Enabled:     true,
		CertFile:    certFile,
		KeyFile:     keyFile,
		DeployToken: t.DeployToken(),
	})
}
