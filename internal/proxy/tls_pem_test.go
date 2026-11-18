package proxy

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/port"
)

// waitListenerReady 等待监听端口可建立 TCP 连接
func waitListenerReady(t *testing.T, port int) {
	t.Helper()
	addr := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("监听器未就绪: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestHTTPSProxyWithPEMCertificate HTTPS 监听器使用 PEM 文本证书即可正常代理
func TestHTTPSProxyWithPEMCertificate(t *testing.T) {
	target, targetAddr := startTargetServer(t, "203.0.113.77\n")
	defer target.Close()

	certPEM, keyPEM, err := GenerateSelfSignedPEM()
	if err != nil {
		t.Fatalf("生成自签证书失败: %v", err)
	}
	if certPEM == "" || keyPEM == "" {
		t.Fatalf("生成的 PEM 不应为空")
	}

	p := port.FindAvailable(22000)
	if p == 0 {
		t.Fatalf("无法找到空闲端口")
	}
	srv, err := BuildServer(config.ProxyListener{
		ID:         "https-pem",
		Protocol:   config.ProxyHTTPS,
		ListenPort: p,
		TLS:        &config.TLSConfig{CertPEM: certPEM, KeyPEM: keyPEM},
	}, &mockDialer{target: targetAddr}, nil)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); srv.Stop() }()
	go srv.Start(ctx)
	waitListenerReady(t, p)

	res, err := Probe(ProbeOptions{
		Protocol: "https",
		Host:     "127.0.0.1",
		Port:     p,
		Target:   "http://" + targetAddr + "/ip",
	})
	if err != nil {
		t.Fatalf("经 HTTPS 代理探测失败: %v", err)
	}
	if res.IP != "203.0.113.77" {
		t.Errorf("出口 IP = %q, want 203.0.113.77", res.IP)
	}
}

// TestHTTPSProxyMissingCertificate 未填写证书内容时应明确报错
func TestHTTPSProxyMissingCertificate(t *testing.T) {
	srv, err := BuildServer(config.ProxyListener{
		ID:         "https-empty",
		Protocol:   config.ProxyHTTPS,
		ListenPort: 0,
		TLS:        &config.TLSConfig{},
	}, &mockDialer{}, nil)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if err := srv.Start(context.Background()); !errors.Is(err, errMissingTLSCert) {
		t.Errorf("Start 应返回 errMissingTLSCert，实际 %v", err)
	}

	// 非法 PEM 也应失败
	srv2, err := BuildServer(config.ProxyListener{
		ID:         "https-bad",
		Protocol:   config.ProxyHTTPS,
		ListenPort: 0,
		TLS:        &config.TLSConfig{CertPEM: "bad", KeyPEM: "bad"},
	}, &mockDialer{}, nil)
	if err != nil {
		t.Fatalf("构建失败: %v", err)
	}
	if err := srv2.Start(context.Background()); err == nil {
		t.Errorf("非法 PEM 应导致 Start 失败")
	}
}
