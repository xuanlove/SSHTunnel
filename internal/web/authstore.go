package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"

	"sshtunnel/internal/persist"
)

// WebAuthSettings 持久化的 WEB 访问密码配置（settings.json）
type WebAuthSettings struct {
	Enabled      *bool  `json:"auth_enabled"`
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
	jwtSecret string
	loadErr   error
	session   context.Context
	revoke    context.CancelFunc
}

var errSessionRevoked = errors.New("会话已失效")

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
	s.session, s.revoke = context.WithCancel(context.Background())
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s
	}
	var cfg WebAuthSettings
	if err == nil {
		err = json.Unmarshal(data, &cfg)
	}
	if err == nil && cfg.Enabled == nil {
		err = errors.New("缺少 WEB 认证开关")
	}
	if err == nil && *cfg.Enabled {
		_, err = bcrypt.Cost([]byte(cfg.PasswordHash))
		if cfg.Username == "" {
			err = errors.New("已启用认证但缺少用户名")
		}
	}
	if err == nil && cfg.JWTSecret != "" {
		decoded, decodeErr := hex.DecodeString(cfg.JWTSecret)
		if decodeErr != nil || len(decoded) != 32 {
			err = errors.New("JWT 密钥无效")
		}
	}
	if err != nil {
		s.loadErr = fmt.Errorf("加载 WEB 访问密码设置失败: %w", err)
		log.Print(s.loadErr)
		return s
	}
	s.enabled = *cfg.Enabled
	s.username = cfg.Username
	s.passHash = []byte(cfg.PasswordHash)
	s.jwtSecret = cfg.JWTSecret
	return s
}

// InitSecret 为首次启动生成并持久化签名密钥。
func (s *AuthStore) InitSecret() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return s.loadErr
	}
	if s.jwtSecret != "" {
		return nil
	}
	key := newAuthSecret()
	if err := s.persistLocked(s.enabled, s.username, s.passHash, key); err != nil {
		return err
	}
	s.jwtSecret = key
	return nil
}

func newAuthSecret() string {
	var key [32]byte
	_, _ = rand.Read(key[:])
	return hex.EncodeToString(key[:])
}

// Secret 返回当前 JWT 签名密钥。
func (s *AuthStore) Secret() []byte {
	_ = s.InitSecret()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return []byte(s.jwtSecret)
}

// sessionState 原子地读取签名密钥及其会话取消信号。
func (s *AuthStore) sessionState() (bool, []byte, context.Context) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled, []byte(s.jwtSecret), s.session
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
	return len(s.verifySecret(username, password)) != 0
}

func (s *AuthStore) verifySecret(username, password string) []byte {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.enabled || len(s.passHash) == 0 || username != s.username ||
		bcrypt.CompareHashAndPassword(s.passHash, []byte(password)) != nil {
		return nil
	}
	return []byte(s.jwtSecret)
}

// Set 设置凭据并启用访问密码。password 为空表示沿用已保存的密码。
func (s *AuthStore) Set(username, password string) error {
	_, _, err := s.setCredentials(nil, username, password)
	return err
}

func (s *AuthStore) setCredentials(session context.Context, username, password string) (string, []byte, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", nil, errors.New("用户名不能为空")
	}
	if strings.Contains(username, ":") {
		return "", nil, errors.New("用户名不能包含冒号")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if session != nil && session != s.session {
		return "", nil, errSessionRevoked
	}
	passHash := s.passHash
	if password != "" {
		if len(password) < 6 {
			return "", nil, errors.New("密码长度至少 6 位")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return "", nil, err
		}
		passHash = hash
	}
	if len(passHash) == 0 {
		return "", nil, errors.New("首次启用必须设置密码")
	}
	if s.enabled && s.username == username && password == "" {
		return username, []byte(s.jwtSecret), nil
	}
	if err := s.updateLocked(true, username, passHash); err != nil {
		return "", nil, err
	}
	return username, []byte(s.jwtSecret), nil
}

// disable 关闭访问密码（保留已保存的凭据，便于再次开启时沿用密码）。
func (s *AuthStore) disable(session context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session != nil && session != s.session {
		return errSessionRevoked
	}
	if !s.enabled {
		return nil
	}
	return s.updateLocked(false, s.username, s.passHash)
}

// initFromCLI 使用启动凭据覆盖运行时认证，不把启动密码写入设置。
func (s *AuthStore) initFromCLI(username, password string) error {
	if strings.TrimSpace(username) == "" || password == "" {
		return errors.New("WEB 启动凭据的用户名和密码不能为空")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.publishLocked(true, username, hash, newAuthSecret())
	return nil
}

func (s *AuthStore) updateLocked(enabled bool, username string, hash []byte) error {
	key := newAuthSecret()
	if err := s.persistLocked(enabled, username, hash, key); err != nil {
		return err
	}
	s.loadErr = nil
	s.publishLocked(enabled, username, hash, key)
	return nil
}

func (s *AuthStore) publishLocked(enabled bool, username string, hash []byte, key string) {
	s.enabled, s.username, s.passHash, s.jwtSecret = enabled, username, hash, key
	s.revoke()
	s.session, s.revoke = context.WithCancel(context.Background())
}

func (s *AuthStore) persistLocked(enabled bool, username string, hash []byte, key string) error {
	if s.path == "" {
		return nil
	}
	cfg := WebAuthSettings{
		Enabled:      &enabled,
		Username:     username,
		PasswordHash: string(hash),
		JWTSecret:    key,
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	// 含密码哈希：原子写入（临时文件 + fsync + rename）且权限收紧为 0600，
	// 避免中断产生残缺的 settings.json 导致配置丢失
	return persist.WriteFileAtomic(s.path, data)
}
