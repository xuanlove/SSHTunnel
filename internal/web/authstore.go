package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// WebAuthSettings 持久化的 WEB 访问密码配置（settings.json）
type WebAuthSettings struct {
	Enabled      bool   `json:"auth_enabled"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash,omitempty"`
	// JWTSecret 持久化的 JWT 签名密钥（hex）。
	// 持久化后服务重启/升级不会使已签发的 Token 失效，避免用户被反复踢回登录页。
	JWTSecret string `json:"jwt_secret,omitempty"`
}

// AuthStore 运行时鉴权状态，支持在系统设置中动态开启/关闭访问密码。
// 凭据以 bcrypt 哈希持久化到 <UserConfigDir>/sshtunnel/settings.json。
type AuthStore struct {
	mu        sync.RWMutex
	path      string // 持久化路径；空字符串表示仅内存（测试用，不落盘）
	enabled   bool
	username  string
	passHash  []byte
	jwtSecret string // JWT 签名密钥（hex），惰性生成并持久化
}

// DefaultAuthStorePath 返回默认持久化路径。
func DefaultAuthStorePath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "sshtunnel", "settings.json")
}

// NewAuthStore 创建鉴权状态。path 非空且文件存在时加载已保存的配置；
// 文件不存在时默认未启用（访问密码默认关闭）。
func NewAuthStore(path string) *AuthStore {
	s := &AuthStore{path: path}
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	var cfg WebAuthSettings
	if json.Unmarshal(data, &cfg) == nil {
		s.enabled = cfg.Enabled
		s.username = cfg.Username
		s.passHash = []byte(cfg.PasswordHash)
		s.jwtSecret = cfg.JWTSecret
	}
	return s
}

// Secret 返回 JWT 签名密钥。首次调用时随机生成并持久化，
// 使服务重启后旧 Token 仍然有效（避免升级重启后被反复踢回登录页）。
func (s *AuthStore) Secret() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jwtSecret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			// 极端情况下退化为时间派生密钥（仍可工作，仅重启后失效）
			s.jwtSecret = fmt.Sprintf("fallback-%d", time.Now().UnixNano())
			return []byte(s.jwtSecret)
		}
		s.jwtSecret = hex.EncodeToString(b)
		if s.path != "" {
			_ = s.persistLocked()
		}
	}
	return []byte(s.jwtSecret)
}

// Enabled 当前是否启用访问密码。
func (s *AuthStore) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// Username 当前用户名。
func (s *AuthStore) Username() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.username
}

// Verify 校验用户名密码（仅在启用状态下有效）。
func (s *AuthStore) Verify(username, password string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled || len(s.passHash) == 0 || username != s.username {
		return false
	}
	return bcrypt.CompareHashAndPassword(s.passHash, []byte(password)) == nil
}

// Set 设置凭据并启用访问密码。password 为空表示沿用已保存的密码。
func (s *AuthStore) Set(username, password string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return errors.New("用户名不能为空")
	}
	if strings.Contains(username, ":") {
		return errors.New("用户名不能包含冒号")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if password != "" {
		if len(password) < 6 {
			return errors.New("密码长度至少 6 位")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		s.passHash = hash
	}
	if len(s.passHash) == 0 {
		return errors.New("首次启用必须设置密码")
	}
	s.username = username
	s.enabled = true
	return s.persistLocked()
}

// Disable 关闭访问密码（保留已保存的凭据，便于再次开启时沿用密码）。
func (s *AuthStore) Disable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = false
	return s.persistLocked()
}

// initFromCLI 使用启动参数 --auth 覆盖为已启用状态。
// 仅内存生效、不落盘、不校验密码长度（保持与历史启动参数行为一致）。
func (s *AuthStore) initFromCLI(username, password string) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = true
	s.username = username
	s.passHash = hash
}

// persistLocked 持久化当前配置（调用方需持有写锁；path 为空时跳过）。
func (s *AuthStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	cfg := WebAuthSettings{
		Enabled:      s.enabled,
		Username:     s.username,
		PasswordHash: string(s.passHash),
		JWTSecret:    s.jwtSecret,
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	// 含密码哈希，权限收紧为 0600
	return os.WriteFile(s.path, data, 0600)
}
