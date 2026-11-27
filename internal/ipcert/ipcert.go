// Package ipcert 提供 Let's Encrypt IP 地址证书的自动签发与续期。
//
// Let's Encrypt 的 IP 证书必须使用 shortlived profile（6 天有效期），
// 且只能通过 http-01 或 tls-alpn-01 验证（不支持 dns-01）。
// 本实现使用 http-01：在配置的端口（默认 80）临时启动 HTTP 服务应答挑战，
// 因此要求该 IP 的公网 80 端口可达。
package ipcert

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	// defaultIssueTimeout 单次签发的最长耗时
	defaultIssueTimeout = 5 * time.Minute
	// renewRatio 剩余有效期低于 1/3 时续期（6 天证书约提前 2 天）
	renewRatio = 2.0 / 3.0
)

// ErrDisabled IP 证书功能未启用
var ErrDisabled = errors.New("IP 证书未启用")

// errIssuing 已有签发任务正在进行
var errIssuing = errors.New("正在签发中，请稍候")

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
	// issueTimeout 单次签发的最长耗时（测试可调整）
	issueTimeout time.Duration
}

// NewManager 创建管理器并加载已保存的设置与证书
func NewManager(dir string, log func(level, msg string)) *Manager {
	if log == nil {
		log = func(string, string) {}
	}
	m := &Manager{dir: dir, log: log, issueTimeout: defaultIssueTimeout}
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
	// 写入文件与更新内存状态放在同一把锁内：与 IssueNow 的 IP 回写串行，
	// 避免设置文件被较旧的内容覆盖
	m.mu.Lock()
	if err := persist.WriteFileAtomic(m.settingsPath(), data); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("保存 IP 证书设置失败: %w", err)
	}
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
// 未启用不签发；无证书、已到续期时间点、或证书 IP 与设置不一致时需要（重新）签发
func (m *Manager) shouldIssueLocked(s Settings) bool {
	if !s.Enabled {
		return false
	}
	if m.cert == nil || m.cert.Leaf == nil {
		return true
	}
	// 已到续期时间点（含停机期间已过期）：立即续期，不等下一个检查周期
	if !time.Now().Before(m.renewAt) {
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

// Start 启动续期检查循环（幂等）。循环开始时会立即检查一次，
// 停机期间已到续期时间点（甚至已过期）的证书会被立即续期
func (m *Manager) Start(parent context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	m.mu.Unlock()

	go m.loop(ctx, renewCheckInterval)
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

// loop 周期检查是否需要续期；开始时先立即检查一次，不等首个 tick
func (m *Manager) loop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if m.renewNeeded() {
			m.issueAsync()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// renewNeeded 判断是否需要（重新）签发；与启动、保存设置时的判断使用同一套规则
func (m *Manager) renewNeeded() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.shouldIssueLocked(m.settings)
}

func (m *Manager) issueAsync() {
	ctx, cancel := context.WithTimeout(context.Background(), m.issueTimeout)
	defer cancel()
	// 「正在签发中」不算失败：进行中的签发完成后会按最新设置决定是否再签发
	if err := m.IssueNow(ctx); err != nil && !errors.Is(err, errIssuing) {
		m.log("error", "IP 证书签发失败: "+err.Error())
	}
}

// IssueNow 立即签发/续期（同步）。ctx 未设置超时（如界面「立即签发」）时使用默认超时，
// 避免 ACME 请求卡住导致「签发中」标记一直不释放、后续续期全部被拒绝
func (m *Manager) IssueNow(ctx context.Context) error {
	m.mu.Lock()
	if !m.settings.Enabled {
		m.mu.Unlock()
		return ErrDisabled
	}
	if m.issuing {
		m.mu.Unlock()
		return errIssuing
	}
	m.issuing = true
	s := m.settings
	m.mu.Unlock()

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.issueTimeout)
		defer cancel()
	}

	ip, err := m.doIssue(ctx, s)

	m.mu.Lock()
	m.issuing = false
	// 签发期间用户把 IP 改成了别的地址：本次签发对应的 IP 已不是用户想要的，
	// 立即按新设置再签发一次（保存设置时触发的签发会因「正在签发中」被跳过）。
	// 这里只比较 IP、不看续期时间点，避免签发结果本身已临近过期时反复重签
	reissue := m.settings.IP != "" && m.settings.IP != ip
	m.mu.Unlock()
	if reissue {
		go m.issueAsync()
	}
	return err
}

// doIssue 执行一次签发：确定 IP → 调用签发函数 → 落盘并更新内存状态。
// 返回本次签发针对的 IP（探测失败时为空）
func (m *Manager) doIssue(ctx context.Context, s Settings) (string, error) {
	m.setError("")
	ip := s.IP
	if ip == "" {
		var err error
		ip, err = m.detectIP(ctx)
		if err != nil {
			m.setError(err.Error())
			return "", err
		}
	}
	if _, err := parsePublicIP(ip); err != nil {
		m.setError(err.Error())
		return ip, err
	}

	m.log("info", fmt.Sprintf("开始为 %s 签发 IP 证书（%s）", ip, s.Directory()))
	certPEM, keyPEM, notAfter, err := m.issueFn(ctx, s, ip)
	if err != nil {
		m.setError(err.Error())
		return ip, err
	}

	if err := os.MkdirAll(m.dir, 0700); err != nil {
		m.setError(err.Error())
		return ip, err
	}
	if err := persist.WriteFileAtomic(m.certPath(), certPEM); err != nil {
		m.setError(err.Error())
		return ip, err
	}
	if err := persist.WriteFileAtomic(m.keyPath(), keyPEM); err != nil {
		m.setError(err.Error())
		return ip, err
	}
	_ = os.Chmod(m.keyPath(), 0600)

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		m.setError(err.Error())
		return ip, err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		m.setError(err.Error())
		return ip, err
	}
	pair.Leaf = leaf

	m.mu.Lock()
	m.cert = &pair
	m.issuedAt = leaf.NotBefore
	m.notAfter = leaf.NotAfter
	m.renewAt = renewTime(leaf.NotBefore, leaf.NotAfter)
	m.lastErr = ""
	// 自动探测得到的 IP 回写设置，便于界面展示与实际签发保持一致。
	// 仅当签发期间用户没有改动 IP 时才回写，且始终写入当前设置而非开始签发时的快照，
	// 以免覆盖用户在签发过程中保存的修改
	if m.settings.IP == s.IP && m.settings.IP != ip {
		m.settings.IP = ip
		// 持锁写入：与 ApplySettings 的保存串行
		if data, err := json.MarshalIndent(m.settings, "", "  "); err == nil {
			_ = persist.WriteFileAtomic(m.settingsPath(), data)
		}
	}
	m.mu.Unlock()

	m.log("info", fmt.Sprintf("IP 证书签发成功：%s，有效期至 %s", ip, notAfter.Format(time.RFC3339)))
	return ip, nil
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

// PEM 返回当前生效的 IP 证书与私钥 PEM 内容（供 certd 读取实际使用的证书）。
// 直接从内存中的证书编码，避免与落盘过程产生不一致的证书/私钥对
func (m *Manager) PEM() (certPEM, keyPEM []byte, err error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.cert == nil {
		return nil, nil, errors.New("IP 证书尚未签发")
	}
	var buf bytes.Buffer
	for _, der := range m.cert.Certificate {
		if err := pem.Encode(&buf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
			return nil, nil, err
		}
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(m.cert.PrivateKey)
	if err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
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
