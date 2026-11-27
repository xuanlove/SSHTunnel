package ipcert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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

func selfSignedIP(t *testing.T, ip string) ([]byte, []byte, time.Time, error) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(6 * 24 * time.Hour),
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
	return certPEM, keyPEM, tmpl.NotAfter, nil
}
