package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/port"
)

// startLocalHTTPProxy 启动一个真实的本地代理监听器用于探测测试
func startLocalHTTPProxy(t *testing.T, protocol config.ProxyProtocol, auth *config.AuthConfig, dialTarget string) (int, func()) {
	t.Helper()
	p := port.FindAvailable(21000)
	if p == 0 {
		t.Fatalf("无法找到空闲端口")
	}
	srv, err := BuildServer(config.ProxyListener{
		ID:         "test-listener",
		Protocol:   protocol,
		ListenPort: p,
		Auth:       auth,
	}, &mockDialer{target: dialTarget}, nil)
	if err != nil {
		t.Fatalf("构建代理服务器失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Start(ctx)

	// 等待监听就绪（Start 在 goroutine 中异步监听）
	addr := "127.0.0.1:" + strconv.Itoa(p)
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("代理 %s 未能就绪: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p, func() { cancel(); srv.Stop() }
}

// startTargetServer 启动返回固定 IP 文本的 HTTP 目标服务器
func startTargetServer(t *testing.T, ip string) (*httptest.Server, string) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, ip)
	}))
	return ts, ts.Listener.Addr().String()
}

// TestProbeViaSOCKS5 验证经 SOCKS5 代理探测目标并拿到出口 IP
func TestProbeViaSOCKS5(t *testing.T) {
	target, targetAddr := startTargetServer(t, "203.0.113.7\n")
	defer target.Close()

	proxyPort, stop := startLocalHTTPProxy(t, config.ProxySOCKS5, nil, targetAddr)
	defer stop()

	res, err := Probe(ProbeOptions{
		Protocol: "socks5",
		Host:     "127.0.0.1",
		Port:     proxyPort,
		Target:   "http://" + targetAddr + "/ip",
	})
	if err != nil {
		t.Fatalf("SOCKS5 探测失败: %v", err)
	}
	if res.IP != "203.0.113.7" {
		t.Errorf("出口 IP = %q, want 203.0.113.7", res.IP)
	}
	if res.ElapsedMS < 0 {
		t.Errorf("耗时不应为负: %d", res.ElapsedMS)
	}
}

// TestProbeViaSOCKS5Auth 验证带用户名密码认证的 SOCKS5 探测
func TestProbeViaSOCKS5Auth(t *testing.T) {
	target, targetAddr := startTargetServer(t, "198.51.100.9\n")
	defer target.Close()

	auth := &config.AuthConfig{Username: "u1", Password: "p1"}
	proxyPort, stop := startLocalHTTPProxy(t, config.ProxySOCKS5, auth, targetAddr)
	defer stop()

	// 正确凭据
	res, err := Probe(ProbeOptions{
		Protocol: "socks5", Host: "127.0.0.1", Port: proxyPort,
		Username: "u1", Password: "p1",
		Target: "http://" + targetAddr + "/ip",
	})
	if err != nil {
		t.Fatalf("带认证 SOCKS5 探测失败: %v", err)
	}
	if res.IP != "198.51.100.9" {
		t.Errorf("出口 IP = %q, want 198.51.100.9", res.IP)
	}

	// 错误凭据应失败
	if _, err := Probe(ProbeOptions{
		Protocol: "socks5", Host: "127.0.0.1", Port: proxyPort,
		Username: "u1", Password: "wrong",
		Target: "http://" + targetAddr + "/ip",
	}); err == nil {
		t.Errorf("错误凭据应探测失败")
	}
}

// TestProbeViaHTTPConnect 验证经 HTTP 代理 CONNECT 隧道探测目标
func TestProbeViaHTTPConnect(t *testing.T) {
	target, targetAddr := startTargetServer(t, "192.0.2.44\n")
	defer target.Close()

	proxyPort, stop := startLocalHTTPProxy(t, config.ProxyHTTP, nil, targetAddr)
	defer stop()

	res, err := Probe(ProbeOptions{
		Protocol: "http", Host: "127.0.0.1", Port: proxyPort,
		Target: "http://" + targetAddr + "/ip",
	})
	if err != nil {
		t.Fatalf("HTTP CONNECT 探测失败: %v", err)
	}
	if res.IP != "192.0.2.44" {
		t.Errorf("出口 IP = %q, want 192.0.2.44", res.IP)
	}
}

// TestProbeConnectionRefused 验证代理未启动时的错误提示
func TestProbeConnectionRefused(t *testing.T) {
	p := port.FindAvailable(23000)
	if _, err := Probe(ProbeOptions{
		Protocol: "socks5", Host: "127.0.0.1", Port: p,
		Target: "http://127.0.0.1:1/ip",
	}); err == nil {
		t.Errorf("代理未监听时应返回错误")
	}
}

// TestProbeInvalidOptions 验证参数校验
func TestProbeInvalidOptions(t *testing.T) {
	if _, err := Probe(ProbeOptions{Protocol: "socks5", Port: 0}); err == nil {
		t.Errorf("端口 0 应报错")
	}
	if _, err := Probe(ProbeOptions{Protocol: "socks5", Port: 1080, Target: "ftp://x/y"}); err == nil {
		t.Errorf("非 http(s) 目标应报错")
	}
}
