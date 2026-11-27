// Package ipcert 提供 Let's Encrypt IP 地址证书的自动签发与续期。
//
// Let's Encrypt 的 IP 证书必须使用 shortlived profile（6 天有效期），
// 且只能通过 http-01 或 tls-alpn-01 验证（不支持 dns-01）。
// 本实现使用 http-01：在配置的端口（默认 80）临时启动 HTTP 服务应答挑战，
// 因此要求该 IP 的公网 80 端口可达。
package ipcert

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sshtunnel/internal/persist"
)

// Let's Encrypt ACME 目录
const (
	LEDirectory        = "https://acme-v02.api.letsencrypt.org/directory"
	LEStagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"

	// shortlivedProfile IP 证书唯一支持的 ACME profile
	shortlivedProfile = "shortlived"

	DefaultHTTPPort = 80

	renewCheckInterval = 30 * time.Minute
	issueTimeout       = 5 * time.Minute
	// renewRatio 剩余有效期低于 1/3 时续期（6 天证书约提前 2 天）
	renewRatio = 2.0 / 3.0
)

// ErrDisabled IP 证书功能未启用
var ErrDisabled = errors.New("IP 证书未启用")

// Settings IP 证书设置
type Settings struct {
	Enabled      bool   `json:"enabled"`
	Email        string `json:"email,omitempty"`         // ACME 账户邮箱（可选）
	DirectoryURL string `json:"directory_url,omitempty"` // 空 = Let's Encrypt 生产环境
	IP           string `json:"ip,omitempty"`            // 要签发的公网 IP；空 = 自动探测
	HTTPPort     int    `json:"http_port,omitempty"`     // http-01 验证端口，默认 80
}

// Directory 返回实际使用的 ACME 目录地址
func (s Settings) Directory() string {
	if strings.TrimSpace(s.DirectoryURL) != "" {
		return strings.TrimSpace(s.DirectoryURL)
	}
	return LEDirectory
}

// Port 返回实际使用的 http-01 端口
func (s Settings) Port() int {
	if s.HTTPPort > 0 {
		return s.HTTPPort
	}
	return DefaultHTTPPort
}

// Status 当前状态（供 API/界面展示）
type Status struct {
	Enabled   bool      `json:"enabled"`
	IP        string    `json:"ip"`
	Issuing   bool      `json:"issuing"`
	IssuedAt  time.Time `json:"issued_at"`
	NotAfter  time.Time `json:"not_after"`
	RenewAt   time.Time `json:"renew_at"`
	LastError string    `json:"last_error"`
	CertPath  string    `json:"cert_path"`
}

// Manager IP 证书管理器
type Manager struct {
	mu       sync.RWMutex
	dir      string
	settings Settings
	cert     *tls.Certificate
	issuedAt time.Time
	notAfter time.Time
	renewAt  time.Time
	lastErr  string
	issuing  bool
	started  bool
	cancel   context.CancelFunc
	log      func(level, msg string)

	// 便于测试注入
	detectIP func(ctx context.Context) (string, error)
	issueFn  func(ctx context.Context, s Settings, ip string) (certPEM, keyPEM []byte, notAfter time.Time, err error)
}

// NewManager 创建管理器并加载已保存的设置与证书
func NewManager(dir string, log func(level, msg string)) *Manager {
	if log == nil {
		log = func(string, string) {}
	}
	m := &Manager{dir: dir, log: log}
	m.detectIP = detectPublicIP
	m.issueFn = m.issueWithACME
	m.loadSettings()
	m.loadCertificate()
	return m
}

func (m *Manager) settingsPath() string { return filepath.Join(m.dir, "settings.json") }
func (m *Manager) certPath() string     { return filepath.Join(m.dir, "cert.pem") }
func (m *Manager) keyPath() string      { return filepath.Join(m.dir, "key.pem") }

func (m *Manager) loadSettings() {
	data, err := os.ReadFile(m.settingsPath())
	if err != nil {
		return
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err == nil {
		m.settings = s
	}
}

// loadCertificate 加载磁盘上的证书（服务重启后无需重新签发）
func (m *Manager) loadCertificate() {
	certPEM, err := os.ReadFile(m.certPath())
	if err != nil {
		return
	}
	keyPEM, err := os.ReadFile(m.keyPath())
	if err != nil {
		return
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		m.log("warn", "已保存的 IP 证书无法解析，将在下次签发时覆盖: "+err.Error())
		return
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return
	}
	pair.Leaf = leaf
	m.cert = &pair
	m.issuedAt = leaf.NotBefore
	m.notAfter = leaf.NotAfter
	m.renewAt = renewTime(leaf.NotBefore, leaf.NotAfter)
}

// renewTime 按证书有效期计算续期时间（剩余 1/3 时）
func renewTime(notBefore, notAfter time.Time) time.Time {
	lifetime := notAfter.Sub(notBefore)
	if lifetime <= 0 {
		return notBefore
	}
	return notBefore.Add(time.Duration(float64(lifetime) * renewRatio))
}

// Settings 返回当前设置副本
func (m *Manager) Settings() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.settings
}

// Status 返回当前状态
func (m *Manager) Status() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := Status{
		Enabled:   m.settings.Enabled,
		IP:        m.settings.IP,
		Issuing:   m.issuing,
		IssuedAt:  m.issuedAt,
		NotAfter:  m.notAfter,
		RenewAt:   m.renewAt,
		LastError: m.lastErr,
		CertPath:  m.certPath(),
	}
	if m.cert != nil && m.cert.Leaf != nil {
		st.IP = certIP(m.cert.Leaf, m.settings.IP)
	}
	return st
}

func certIP(leaf *x509.Certificate, fallback string) string {
	if len(leaf.IPAddresses) > 0 {
		return leaf.IPAddresses[0].String()
	}
	return fallback
}

// ApplySettings 保存设置；启用后在缺少有效证书时异步签发
func (m *Manager) ApplySettings(s Settings) error {
	s.Email = strings.TrimSpace(s.Email)
	s.IP = strings.TrimSpace(s.IP)
	s.DirectoryURL = strings.TrimSpace(s.DirectoryURL)
	if s.IP != "" {
		if _, err := parsePublicIP(s.IP); err != nil {
			return err
		}
	}
	if p := s.Port(); p < 1 || p > 65535 {
		return fmt.Errorf("http-01 验证端口无效: %d", p)
	}
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return fmt.Errorf("创建证书目录失败: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := persist.WriteFileAtomic(m.settingsPath(), data); err != nil {
		return fmt.Errorf("保存 IP 证书设置失败: %w", err)
	}

	m.mu.Lock()
	m.settings = s
	m.lastErr = ""
	needIssue := m.shouldIssueLocked(s)
	m.mu.Unlock()

	if needIssue {
		go m.issueAsync()
	}
	return nil
}

// shouldIssueLocked 判断是否需要签发（调用方需持有锁）：
// 未启用不签发；无证书或证书 IP 与设置不一致时需要（重新）签发
func (m *Manager) shouldIssueLocked(s Settings) bool {
	if !s.Enabled {
		return false
	}
	if m.cert == nil || m.cert.Leaf == nil {
		return true
	}
	if s.IP == "" {
		return false
	}
	want, err := parsePublicIP(s.IP)
	if err != nil {
		return false
	}
	for _, ip := range m.cert.Leaf.IPAddresses {
		if ip.Equal(want) {
			return false
		}
	}
	return true
}

// Start 启动续期检查循环（幂等）
func (m *Manager) Start(parent context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	needIssue := m.shouldIssueLocked(m.settings)
	m.mu.Unlock()

	if needIssue {
		go m.issueAsync()
	}
	go m.loop(ctx)
}

// Stop 停止续期循环
func (m *Manager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.started = false
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) loop(ctx context.Context) {
	t := time.NewTicker(renewCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if m.renewNeeded() {
				m.issueAsync()
			}
		}
	}
}

// renewNeeded 判断是否需要（重新）签发
func (m *Manager) renewNeeded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.settings.Enabled {
		return false
	}
	if m.cert == nil {
		return true
	}
	return !time.Now().Before(m.renewAt)
}

func (m *Manager) issueAsync() {
	ctx, cancel := context.WithTimeout(context.Background(), issueTimeout)
	defer cancel()
	if err := m.IssueNow(ctx); err != nil {
		m.log("error", "IP 证书签发失败: "+err.Error())
	}
}

// IssueNow 立即签发/续期（同步）
func (m *Manager) IssueNow(ctx context.Context) error {
	m.mu.Lock()
	if !m.settings.Enabled {
		m.mu.Unlock()
		return ErrDisabled
	}
	if m.issuing {
		m.mu.Unlock()
		return errors.New("正在签发中，请稍候")
	}
	m.issuing = true
	s := m.settings
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.issuing = false
		m.mu.Unlock()
	}()

	m.setError("")
	ip := s.IP
	if ip == "" {
		var err error
		ip, err = m.detectIP(ctx)
		if err != nil {
			m.setError(err.Error())
			return err
		}
	}
	if _, err := parsePublicIP(ip); err != nil {
		m.setError(err.Error())
		return err
	}

	m.log("info", fmt.Sprintf("开始为 %s 签发 IP 证书（%s）", ip, s.Directory()))
	certPEM, keyPEM, notAfter, err := m.issueFn(ctx, s, ip)
	if err != nil {
		m.setError(err.Error())
		return err
	}

	if err := os.MkdirAll(m.dir, 0700); err != nil {
		m.setError(err.Error())
		return err
	}
	if err := persist.WriteFileAtomic(m.certPath(), certPEM); err != nil {
		m.setError(err.Error())
		return err
	}
	if err := persist.WriteFileAtomic(m.keyPath(), keyPEM); err != nil {
		m.setError(err.Error())
		return err
	}
	_ = os.Chmod(m.keyPath(), 0600)

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		m.setError(err.Error())
		return err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		m.setError(err.Error())
		return err
	}
	pair.Leaf = leaf

	m.mu.Lock()
	m.cert = &pair
	m.issuedAt = leaf.NotBefore
	m.notAfter = leaf.NotAfter
	m.renewAt = renewTime(leaf.NotBefore, leaf.NotAfter)
	m.settings.IP = ip
	m.lastErr = ""
	m.mu.Unlock()

	// 自动探测得到的 IP 回写到设置文件，便于界面展示与实际签发保持一致
	if s.IP != ip {
		s.IP = ip
		if data, err := json.MarshalIndent(s, "", "  "); err == nil {
			_ = persist.WriteFileAtomic(m.settingsPath(), data)
		}
	}

	m.log("info", fmt.Sprintf("IP 证书签发成功：%s，有效期至 %s", ip, notAfter.Format(time.RFC3339)))
	return nil
}

func (m *Manager) setError(msg string) {
	m.mu.Lock()
	m.lastErr = msg
	m.mu.Unlock()
}

// TLSCertificate 返回当前证书，供 HTTPS 代理监听器动态使用（续期后无需重启）
func (m *Manager) TLSCertificate() (tls.Certificate, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.settings.Enabled {
		return tls.Certificate{}, ErrDisabled
	}
	if m.cert == nil {
		return tls.Certificate{}, errors.New("IP 证书尚未签发，请在系统设置中点击「立即签发」或稍候重试")
	}
	return *m.cert, nil
}

// parsePublicIP 解析并校验公网 IP（拒绝私网/回环/链路本地/未指定/组播）
func parsePublicIP(s string) (net.IP, error) {
	ip := net.ParseIP(strings.Trim(s, "[] "))
	if ip == nil {
		return nil, fmt.Errorf("无效的 IP 地址: %s", s)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return nil, fmt.Errorf("IP 证书只能用于公网地址: %s", ip)
	}
	return ip, nil
}

// detectPublicIP 通过公共接口探测本机公网出口 IP
func detectPublicIP(ctx context.Context) (string, error) {
	endpoints := []string{
		"https://api.ip.sb/ip",
		"https://ifconfig.me/ip",
		"https://ipinfo.io/ip",
	}
	client := &http.Client{Timeout: 6 * time.Second}
	var lastErr error
	for _, u := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "sshtunnel-ipcert")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		ip := strings.TrimSpace(strings.SplitN(string(body), "\n", 2)[0])
		if _, err := parsePublicIP(ip); err != nil {
			lastErr = err
			continue
		}
		return ip, nil
	}
	if lastErr == nil {
		lastErr = errors.New("所有探测地址均不可用")
	}
	return "", fmt.Errorf("自动探测公网 IP 失败（请手动填写）: %w", lastErr)
}
