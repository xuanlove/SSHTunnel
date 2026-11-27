package ipcert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRenewTime(t *testing.T) {
	notBefore := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notAfter := notBefore.Add(6 * 24 * time.Hour)
	got := renewTime(notBefore, notAfter)
	want := notBefore.Add(4 * 24 * time.Hour) // 剩余 1/3 时续期
	if !got.Equal(want) {
		t.Errorf("续期时间错误: got %s, want %s", got, want)
	}
}

func TestParsePublicIP(t *testing.T) {
	for _, ok := range []string{"1.2.3.4", "2606:4700::6810:84e5"} {
		if _, err := parsePublicIP(ok); err != nil {
			t.Errorf("公网 IP %q 应通过: %v", ok, err)
		}
	}
	for _, bad := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "::1", "fe80::1", "0.0.0.0", "not-an-ip", ""} {
		if _, err := parsePublicIP(bad); err == nil {
			t.Errorf("地址 %q 应被拒绝", bad)
		}
	}
}

func TestChallengeServer(t *testing.T) {
	cs, err := startChallengeServer(0)
	if err != nil {
		t.Fatalf("启动挑战服务失败: %v", err)
	}
	defer cs.Close()

	port := cs.ln.Addr().(*net.TCPAddr).Port
	cs.setToken("tok123", "key-auth-value")

	resp, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/.well-known/acme-challenge/tok123")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "key-auth-value" {
		t.Errorf("应答错误: status=%d body=%q", resp.StatusCode, body)
	}

	// 未知 token 返回 404
	resp2, err := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/.well-known/acme-challenge/missing")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("未知 token 应返回 404, got %d", resp2.StatusCode)
	}
}

func TestApplySettingsValidatesIP(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	if err := m.ApplySettings(Settings{Enabled: true, IP: "10.0.0.1"}); err == nil {
		t.Errorf("私网 IP 应被拒绝")
	}
	if err := m.ApplySettings(Settings{Enabled: true, IP: "1.2.3.4", HTTPPort: 70000}); err == nil {
		t.Errorf("非法端口应被拒绝")
	}
	if err := m.ApplySettings(Settings{Enabled: false, IP: ""}); err != nil {
		t.Errorf("关闭状态下保持空 IP 应允许: %v", err)
	}
}

func TestManagerIssueAndProvider(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir, nil)
	if err := m.ApplySettings(Settings{Enabled: false, IP: "1.2.3.4", HTTPPort: 80}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	m.mu.Lock()
	m.settings.Enabled = true
	m.mu.Unlock()

	var calledIP string
	m.issueFn = func(ctx context.Context, s Settings, ip string) ([]byte, []byte, time.Time, error) {
		calledIP = ip
		return selfSignedIP(t, ip)
	}

	if err := m.IssueNow(context.Background()); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if calledIP != "1.2.3.4" {
		t.Errorf("签发 IP 错误: %s", calledIP)
	}

	cert, err := m.TLSCertificate()
	if err != nil {
		t.Fatalf("获取证书失败: %v", err)
	}
	if len(cert.Certificate) == 0 || cert.Leaf == nil {
		t.Fatalf("证书内容为空")
	}
	if cert.Leaf.IPAddresses[0].String() != "1.2.3.4" {
		t.Errorf("证书 IP 错误: %v", cert.Leaf.IPAddresses)
	}

	// 私钥文件权限必须是 0600
	info, err := os.Stat(filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatalf("私钥文件不存在: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("私钥权限应为 0600, got %o", info.Mode().Perm())
	}

	// 状态应反映过期时间与续期时间
	st := m.Status()
	if st.NotAfter.IsZero() || st.RenewAt.IsZero() || st.IP != "1.2.3.4" {
		t.Errorf("状态不完整: %+v", st)
	}

	// 重启后应能加载已保存证书（此处仅验证证书加载，不触发真实签发）
	m2 := NewManager(dir, nil)
	if m2.cert == nil || m2.cert.Leaf == nil {
		t.Errorf("重启后应能加载已保存证书")
	}
}

func TestShouldIssueOnIPChange(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	// 无证书时启用 → 需要签发
	if !m.shouldIssueLocked(Settings{Enabled: true}) {
		t.Errorf("无证书时启用应需要签发")
	}
	if m.shouldIssueLocked(Settings{Enabled: false}) {
		t.Errorf("未启用不应签发")
	}

	// 已有 1.2.3.4 的证书：相同 IP 不重签，变更 IP 重签
	m.issueFn = func(ctx context.Context, s Settings, ip string) ([]byte, []byte, time.Time, error) {
		return selfSignedIP(t, ip)
	}
	m.mu.Lock()
	m.settings = Settings{Enabled: true, IP: "1.2.3.4"}
	m.mu.Unlock()
	if err := m.IssueNow(context.Background()); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if m.shouldIssueLocked(Settings{Enabled: true, IP: "1.2.3.4"}) {
		t.Errorf("相同 IP 不应重复签发")
	}
	if !m.shouldIssueLocked(Settings{Enabled: true, IP: "5.6.7.8"}) {
		t.Errorf("IP 变更后应重新签发")
	}
}

func TestManagerDisabledReturnsError(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	if _, err := m.TLSCertificate(); err != ErrDisabled {
		t.Errorf("未启用时应返回 ErrDisabled, got %v", err)
	}
}

// ----- 续期触发 -----

// writeState 写入证书与设置文件，模拟进程重启前的落盘状态
func writeState(t *testing.T, dir, ip string, notBefore, notAfter time.Time, s Settings) {
	t.Helper()
	certPEM, keyPEM, _, err := selfSignedIPValid(t, ip, notBefore, notAfter)
	if err != nil {
		t.Fatalf("生成证书失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0600); err != nil {
		t.Fatalf("写入证书失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0600); err != nil {
		t.Fatalf("写入私钥失败: %v", err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("编码设置失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0600); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
}

// waitFor 轮询等待条件成立（异步签发的结果校验）
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待条件超时（%s）", timeout)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// freshIssuer 返回一个签发「新鲜」证书的签发函数，以及调用次数读取函数
func freshIssuer(t *testing.T) (func(ctx context.Context, s Settings, ip string) ([]byte, []byte, time.Time, error), func() int32) {
	t.Helper()
	var calls atomic.Int32
	return func(ctx context.Context, s Settings, ip string) ([]byte, []byte, time.Time, error) {
		calls.Add(1)
		return selfSignedIP(t, ip)
	}, calls.Load
}

// TestShouldIssueWhenRenewalDue 已到续期时间点（含已过期）的证书需要续期
func TestShouldIssueWhenRenewalDue(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	// 让签发结果本身就是一张已过期的证书
	m.issueFn = func(ctx context.Context, s Settings, ip string) ([]byte, []byte, time.Time, error) {
		return selfSignedIPValid(t, ip, time.Now().Add(-8*24*time.Hour), time.Now().Add(-time.Hour))
	}
	m.mu.Lock()
	m.settings = Settings{Enabled: true, IP: "1.2.3.4"}
	m.mu.Unlock()
	if err := m.IssueNow(context.Background()); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if !m.shouldIssueLocked(Settings{Enabled: true, IP: "1.2.3.4"}) {
		t.Errorf("已过期证书应需要续期")
	}
	if m.shouldIssueLocked(Settings{Enabled: false}) {
		t.Errorf("未启用不应续期")
	}
}

// TestStartRenewsStaleCertImmediately 停机期间证书已过期：重启后应立即续期，
// 而不是等首个定时检查（30 分钟后）
func TestStartRenewsStaleCertImmediately(t *testing.T) {
	const ip = "1.2.3.4"
	dir := t.TempDir()
	writeState(t, dir, ip, time.Now().Add(-8*24*time.Hour), time.Now().Add(-time.Hour), Settings{Enabled: true, IP: ip})

	m := NewManager(dir, nil)
	issueFn, calls := freshIssuer(t)
	m.issueFn = issueFn
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	waitFor(t, 2*time.Second, func() bool {
		st := m.Status()
		return calls() == 1 && st.NotAfter.After(time.Now()) && st.LastError == ""
	})
}

// TestApplySettingsRenewsStaleCert 关闭一段时间后重新启用：证书已过期时应立即续期
func TestApplySettingsRenewsStaleCert(t *testing.T) {
	const ip = "1.2.3.4"
	dir := t.TempDir()
	writeState(t, dir, ip, time.Now().Add(-8*24*time.Hour), time.Now().Add(-time.Hour), Settings{Enabled: false, IP: ip})

	m := NewManager(dir, nil)
	issueFn, calls := freshIssuer(t)
	m.issueFn = issueFn
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.Start(ctx)

	// 未启用：启动后不会签发
	time.Sleep(30 * time.Millisecond)
	if calls() != 0 {
		t.Fatalf("未启用时不应签发")
	}

	if err := m.ApplySettings(Settings{Enabled: true, IP: ip}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return m.Status().NotAfter.After(time.Now()) })
}

// TestLoopRenewsOnlyWhenDue 续期循环：开始时立即检查一次；
// 新证书未到续期时间点时，后续 tick 不重复签发
func TestLoopRenewsOnlyWhenDue(t *testing.T) {
	const ip = "1.2.3.4"
	dir := t.TempDir()
	writeState(t, dir, ip, time.Now().Add(-8*24*time.Hour), time.Now().Add(-time.Hour), Settings{Enabled: true, IP: ip})

	m := NewManager(dir, nil)
	issueFn, calls := freshIssuer(t)
	m.issueFn = issueFn
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.loop(ctx, 5*time.Millisecond)

	waitFor(t, 2*time.Second, func() bool {
		return calls() >= 1 && m.Status().NotAfter.After(time.Now())
	})
	time.Sleep(50 * time.Millisecond) // 再经过约 10 个 tick
	if got := calls(); got != 1 {
		t.Errorf("未到续期时间点不应重复签发, 实际签发 %d 次", got)
	}
}

// ----- 签发期间设置变更 -----

// blockingIssuer 让首次签发（自动探测得到的 1.2.3.4）阻塞直到 release 关闭，并记录签发过的 IP
func blockingIssuer(t *testing.T, m *Manager) (entered <-chan struct{}, release chan struct{}, issued func() []string) {
	t.Helper()
	m.mu.Lock()
	m.settings = Settings{Enabled: true, IP: ""} // 留空 = 自动探测
	m.mu.Unlock()
	m.detectIP = func(context.Context) (string, error) { return "1.2.3.4", nil }

	enteredCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var mu sync.Mutex
	var ips []string
	m.issueFn = func(ctx context.Context, s Settings, ip string) ([]byte, []byte, time.Time, error) {
		mu.Lock()
		ips = append(ips, ip)
		mu.Unlock()
		if ip == "1.2.3.4" {
			close(enteredCh)
			<-releaseCh
		}
		return selfSignedIP(t, ip)
	}
	return enteredCh, releaseCh, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), ips...)
	}
}

// readSettings 读取磁盘上的设置文件
func readSettings(t *testing.T, dir string) Settings {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("解析设置失败: %v", err)
	}
	return s
}

// TestIssueNowSettingsChangedDuringIssuance 签发过程中用户修改设置：
// 以用户的新设置为准（不被开始签发时的快照覆盖），并按新 IP 再签发一次
func TestIssueNowSettingsChangedDuringIssuance(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	entered, release, issued := blockingIssuer(t, m)

	done := make(chan error, 1)
	go func() { done <- m.IssueNow(context.Background()) }()
	<-entered

	want := Settings{Enabled: true, IP: "5.6.7.8", Email: "new@example.com"}
	if err := m.ApplySettings(want); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("首次签发失败: %v", err)
	}

	// 证书最终对应新 IP
	waitFor(t, 2*time.Second, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return m.cert != nil && m.cert.Leaf != nil && m.cert.Leaf.IPAddresses[0].String() == "5.6.7.8"
	})
	if got := issued(); len(got) != 2 || got[0] != "1.2.3.4" || got[1] != "5.6.7.8" {
		t.Errorf("应先按旧设置、再按新 IP 各签发一次, 实际 %v", got)
	}
	// 内存与磁盘上的设置均为用户保存的新设置
	if got := m.Settings(); got != want {
		t.Errorf("内存设置应保持用户修改: got %+v, want %+v", got, want)
	}
	if got := readSettings(t, m.dir); got != want {
		t.Errorf("磁盘设置被旧内容覆盖: %+v", got)
	}
}

// TestIssueNowDisabledDuringIssuance 签发过程中用户关闭功能：
// 关闭状态不被回写覆盖（重启后仍为关闭），且不再触发签发
func TestIssueNowDisabledDuringIssuance(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	entered, release, issued := blockingIssuer(t, m)

	done := make(chan error, 1)
	go func() { done <- m.IssueNow(context.Background()) }()
	<-entered
	if err := m.ApplySettings(Settings{Enabled: false}); err != nil {
		t.Fatalf("保存设置失败: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("首次签发失败: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return !m.Status().Issuing })

	if m.Settings().Enabled {
		t.Errorf("内存中的关闭状态不应被回写覆盖")
	}
	if got := readSettings(t, m.dir); got.Enabled {
		t.Errorf("磁盘上的关闭状态不应被回写覆盖: %+v", got)
	}
	if _, err := m.TLSCertificate(); err != ErrDisabled {
		t.Errorf("关闭后不应提供证书, got %v", err)
	}
	if got := issued(); len(got) != 1 {
		t.Errorf("关闭后不应再签发, 实际签发 %v", got)
	}
}

// TestIssueNowWritesBackDetectedIP 设置未变更时，自动探测得到的 IP 仍会回写到内存与磁盘
func TestIssueNowWritesBackDetectedIP(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	m.mu.Lock()
	m.settings = Settings{Enabled: true}
	m.mu.Unlock()
	m.detectIP = func(context.Context) (string, error) { return "1.2.3.4", nil }
	issueFn, _ := freshIssuer(t)
	m.issueFn = issueFn

	if err := m.IssueNow(context.Background()); err != nil {
		t.Fatalf("签发失败: %v", err)
	}
	if got := m.Settings().IP; got != "1.2.3.4" {
		t.Errorf("内存设置应回写探测到的 IP, got %q", got)
	}
	if got := readSettings(t, m.dir); got.IP != "1.2.3.4" {
		t.Errorf("磁盘设置应回写探测到的 IP, got %+v", got)
	}
}

// ----- 签发超时 -----

// TestIssueNowAppliesDefaultTimeout 调用方未设置超时时使用默认超时：
// ACME 请求卡住不会让「签发中」标记一直占着
func TestIssueNowAppliesDefaultTimeout(t *testing.T) {
	m := NewManager(t.TempDir(), nil)
	m.issueTimeout = 50 * time.Millisecond
	m.mu.Lock()
	m.settings = Settings{Enabled: true, IP: "1.2.3.4"}
	m.mu.Unlock()
	m.issueFn = func(ctx context.Context, s Settings, ip string) ([]byte, []byte, time.Time, error) {
		<-ctx.Done() // 模拟 ACME 请求卡住
		return nil, nil, time.Time{}, ctx.Err()
	}

	// 未设置默认超时时该调用永不返回：限时等待，避免用例挂死
	done := make(chan error, 1)
	go func() { done <- m.IssueNow(context.Background()) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("IssueNow 未应用默认超时，一直未返回")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应因超时而失败, got %v", err)
	}
	if m.Status().Issuing {
		t.Errorf("超时后「签发中」标记应释放")
	}

	// 标记已释放：可以再次发起并正常完成
	issueFn, _ := freshIssuer(t)
	m.issueFn = issueFn
	if err := m.IssueNow(context.Background()); err != nil {
		t.Fatalf("超时后再次签发应成功: %v", err)
	}
}

func selfSignedIP(t *testing.T, ip string) ([]byte, []byte, time.Time, error) {
	t.Helper()
	return selfSignedIPValid(t, ip, time.Now().Add(-time.Hour), time.Now().Add(6*24*time.Hour))
}

// selfSignedIPValid 生成指定有效期的自签 IP 证书
func selfSignedIPValid(t *testing.T, ip string, notBefore, notAfter time.Time) ([]byte, []byte, time.Time, error) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		IPAddresses:  []net.IP{net.ParseIP(ip)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, notAfter, nil
}
