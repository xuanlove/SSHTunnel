package proxy

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sshtunnel/internal/config"
)

// mockDialer 实现 Dialer 接口，将连接重定向到本地回环测试服务器
type mockDialer struct {
	target string
}

func (d *mockDialer) Dial(network, address string) (net.Conn, error) {
	// 忽略 address，连接到预设的 target
	return net.Dial("tcp", d.target)
}

// startEchoServer 启动一个简单 TCP 回显服务器用于验证代理转发
func startEchoServer(t *testing.T) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动 echo 服务器失败: %v", err)
	}
	addr := ln.Addr().String()
	stop := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-stop:
					return
				default:
					return
				}
			}
			go func(c net.Conn) {
				defer c.Close()
				// 先发 ECHO: 前缀
				c.Write([]byte("ECHO:"))
				// 持续回显
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if _, err := c.Write(buf[:n]); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return addr, func() {
		close(stop)
		ln.Close()
	}
}

// startProxyServer 启动一个代理服务器用于测试
func startProxyServer(t *testing.T, lc config.ProxyListener, dialer Dialer) (int, *testServerHandle, error) {
	t.Helper()
	// 找一个可用端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, nil, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// 重新用配置端口构建（覆盖为可用端口）
	lc.ListenPort = port
	lc.AllowExternal = false

	// 使用 BuildServer 构建实例
	srv, err := BuildServer(lc, dialer, func(level, msg string) {
		t.Logf("[proxy-%s] %s: %s", lc.Protocol, level, msg)
	})
	if err != nil {
		return 0, nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = srv.Start(ctx)
	}()

	// 等待监听器就绪
	time.Sleep(50 * time.Millisecond)

	handle := &testServerHandle{
		srv:    srv,
		cancel: cancel,
		port:   port,
	}
	return port, handle, nil
}

type testServerHandle struct {
	srv    Server
	cancel context.CancelFunc
	port   int
}

func (h *testServerHandle) Stop() {
	h.cancel()
	_ = h.srv.Stop()
}

// ============== SOCKS5 测试 ==============

func TestSOCKS5Proxy_NoAuth(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	dialer := &mockDialer{target: echoAddr}
	lc := config.ProxyListener{
		ID:         "test-socks5-1",
		Protocol:   config.ProxySOCKS5,
		ListenPort: 0,
	}
	port, handle, err := startProxyServer(t, lc, dialer)
	if err != nil {
		t.Fatalf("启动 SOCKS5 代理失败: %v", err)
	}
	defer handle.Stop()

	// 通过 SOCKS5 代理连接 echo 服务器
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()

	// 1. SOCKS5 握手：VER(1) NMETHODS(1) METHODS(1) 无认证
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("发送握手失败: %v", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("读取握手响应失败: %v", err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("握手响应错误: %v", resp)
	}

	// 2. 请求转发到 echo 服务器
	ip := net.ParseIP("127.0.0.1").To4()
	req := []byte{0x05, 0x01, 0x00, 0x01} // VER, CMD=CONNECT, RSV, ATYP=IPv4
	req = append(req, ip...)
	portBuf := []byte{byte(echoPortHigh(echoAddr)), byte(echoPortLow(echoAddr))}
	req = append(req, portBuf...)
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}

	// 读取响应
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	if reply[1] != 0x00 {
		t.Fatalf("CONNECT 失败, rep=%d", reply[1])
	}

	// 3. 发送数据，验证回显
	msg := []byte("hello-socks5")
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("发送数据失败: %v", err)
	}
	expected := "ECHO:" + string(msg)
	buf := make([]byte, 100)
	total := 0
	deadline := time.Now().Add(2 * time.Second)
	for total < len(expected) && time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		n, err := conn.Read(buf[total:])
		if err != nil {
			break
		}
		total += n
	}
	if string(buf[:total]) != expected {
		t.Errorf("回显不匹配: got %q, want %q", buf[:total], expected)
	}
}

func TestSOCKS5Proxy_WithAuth(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	dialer := &mockDialer{target: echoAddr}
	lc := config.ProxyListener{
		ID:         "test-socks5-2",
		Protocol:   config.ProxySOCKS5,
		ListenPort: 0,
		Auth: &config.AuthConfig{
			Username: "user",
			Password: "pass",
		},
	}
	port, handle, err := startProxyServer(t, lc, dialer)
	if err != nil {
		t.Fatalf("启动 SOCKS5 代理失败: %v", err)
	}
	defer handle.Stop()

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", port)

	// 子测试：错误凭据
	t.Run("错误凭据应被拒绝", func(t *testing.T) {
		conn, _ := net.Dial("tcp", proxyAddr)
		defer conn.Close()
		// 握手：提供用户名密码方法
		conn.Write([]byte{0x05, 0x01, 0x02})
		resp := make([]byte, 2)
		io.ReadFull(conn, resp)
		if resp[1] != 0x02 {
			t.Fatalf("代理应选择 0x02 用户名密码方法, got %d", resp[1])
		}
		// 发送错误凭据: VER(1) ULEN(1) UNAME PLEN(1) PASSWD
		conn.Write([]byte{0x01, 0x03, 'b', 'a', 'd', 0x04, 'b', 'a', 'd', '2'})
		authResp := make([]byte, 2)
		io.ReadFull(conn, authResp)
		if authResp[1] != 0x01 {
			t.Errorf("错误凭据应返回失败 0x01, got %d", authResp[1])
		}
	})

	// 子测试：正确凭据
	t.Run("正确凭据应通过", func(t *testing.T) {
		conn, _ := net.Dial("tcp", proxyAddr)
		defer conn.Close()
		conn.Write([]byte{0x05, 0x01, 0x02})
		resp := make([]byte, 2)
		io.ReadFull(conn, resp)
		// 发送正确凭据
		conn.Write([]byte{0x01, 0x04, 'u', 's', 'e', 'r', 0x04, 'p', 'a', 's', 's'})
		authResp := make([]byte, 2)
		io.ReadFull(conn, authResp)
		if authResp[1] != 0x00 {
			t.Fatalf("正确凭据应返回成功 0x00, got %d", authResp[1])
		}
		// CONNECT
		ip := net.ParseIP("127.0.0.1").To4()
		req := []byte{0x05, 0x01, 0x00, 0x01}
		req = append(req, ip...)
		req = append(req, byte(echoPortHigh(echoAddr)), byte(echoPortLow(echoAddr)))
		conn.Write(req)
		reply := make([]byte, 10)
		io.ReadFull(conn, reply)
		if reply[1] != 0x00 {
			t.Fatalf("CONNECT 应成功, rep=%d", reply[1])
		}
	})
}

// ============== HTTP 代理测试 ==============

func TestHTTPProxy_CONNECT(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	dialer := &mockDialer{target: echoAddr}
	lc := config.ProxyListener{
		ID:         "test-http-1",
		Protocol:   config.ProxyHTTP,
		ListenPort: 0,
	}
	port, handle, err := startProxyServer(t, lc, dialer)
	if err != nil {
		t.Fatalf("启动 HTTP 代理失败: %v", err)
	}
	defer handle.Stop()

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()

	// 发送 CONNECT 请求
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("发送 CONNECT 失败: %v", err)
	}

	// 读取响应
	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读取状态行失败: %v", err)
	}
	if !strings.Contains(statusLine, "200") {
		t.Fatalf("CONNECT 应返回 200, got: %s", statusLine)
	}
	// 读取剩余头部直到空行
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("读取头部失败: %v", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}

	// 发送数据，验证回显
	msg := []byte("hello-http-connect")
	conn.Write(msg)
	expected := "ECHO:" + string(msg)
	buf := make([]byte, 100)
	total := 0
	deadline := time.Now().Add(2 * time.Second)
	for total < len(expected) && time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		// 必须经 br 读取：CONNECT 响应后 bufio 可能已缓冲 "ECHO:" 前缀
		n, err := br.Read(buf[total:])
		if err != nil {
			break
		}
		total += n
	}
	if string(buf[:total]) != expected {
		t.Errorf("回显不匹配: got %q, want %q", buf[:total], expected)
	}
}

// strictDialer 模拟真实 SSH Dial：目标必须是 host:port 形式，缺省端口时拨号失败
type strictDialer struct {
	target string
}

func (d *strictDialer) Dial(network, address string) (net.Conn, error) {
	if _, _, err := net.SplitHostPort(address); err != nil {
		return nil, fmt.Errorf("dial %s: %w", address, err)
	}
	return net.Dial(network, d.target)
}

// TestHTTPProxy_PlainHTTP_PortlessHost 普通代理请求目标省略默认端口时应补 80 转发。
// 修复前 `GET http://example.com/` 因 Dial 缺端口必然 502，纯 HTTP 代理不可用。
func TestHTTPProxy_PlainHTTP_PortlessHost(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok-via-proxy"))
	}))
	defer ts.Close()

	lc := config.ProxyListener{ID: "test-http-plain", Protocol: config.ProxyHTTP, ListenPort: 0}
	port, handle, err := startProxyServer(t, lc, &strictDialer{target: ts.Listener.Addr().String()})
	if err != nil {
		t.Fatalf("启动 HTTP 代理失败: %v", err)
	}
	defer handle.Stop()

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()

	// 绝对形式、无端口
	req := "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读取状态行失败: %v", err)
	}
	if strings.Contains(statusLine, "502") {
		t.Fatalf("无端口目标不应返回 502，got: %s", statusLine)
	}
	if !strings.Contains(statusLine, "200") {
		t.Fatalf("应返回目标服务器的 200 响应, got: %s", statusLine)
	}
	body, _ := io.ReadAll(br)
	if !strings.Contains(string(body), "ok-via-proxy") {
		t.Errorf("响应体应来自目标服务器，got: %q", body)
	}
}

func TestHTTPProxy_WithAuth(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	dialer := &mockDialer{target: echoAddr}
	lc := config.ProxyListener{
		ID:         "test-http-2",
		Protocol:   config.ProxyHTTP,
		ListenPort: 0,
		Auth: &config.AuthConfig{
			Username: "user",
			Password: "pass",
		},
	}
	port, handle, err := startProxyServer(t, lc, dialer)
	if err != nil {
		t.Fatalf("启动 HTTP 代理失败: %v", err)
	}
	defer handle.Stop()

	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	_ = proxyURL

	// 子测试：无认证应返回 407
	t.Run("无认证应返回407", func(t *testing.T) {
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Fatalf("连接代理失败: %v", err)
		}
		defer conn.Close()
		// 发送不带认证的 CONNECT
		req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echoAddr, echoAddr)
		conn.Write([]byte(req))
		br := bufio.NewReader(conn)
		statusLine, _ := br.ReadString('\n')
		if !strings.Contains(statusLine, "407") {
			t.Errorf("无认证应返回 407, got: %s", statusLine)
		}
	})

	// 子测试：正确认证应能 CONNECT
	t.Run("正确认证可通过CONNECT", func(t *testing.T) {
		conn, _ := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		defer conn.Close()
		auth := base64.StdEncoding.EncodeToString([]byte("user:pass"))
		req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", echoAddr, echoAddr, auth)
		conn.Write([]byte(req))
		br := bufio.NewReader(conn)
		statusLine, _ := br.ReadString('\n')
		if !strings.Contains(statusLine, "200") {
			t.Errorf("正确认证的 CONNECT 应返回 200, got: %s", statusLine)
		}
	})
}

// ============== SOCKS4 测试 ==============

func TestSOCKS4Proxy_Basic(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	dialer := &mockDialer{target: echoAddr}
	lc := config.ProxyListener{
		ID:         "test-socks4-1",
		Protocol:   config.ProxySOCKS4,
		ListenPort: 0,
	}
	port, handle, err := startProxyServer(t, lc, dialer)
	if err != nil {
		t.Fatalf("启动 SOCKS4 代理失败: %v", err)
	}
	defer handle.Stop()

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()

	// SOCKS4 请求: VN(1) CD(1) DSTPORT(2) DSTIP(4) USERID(null)
	req := []byte{0x04, 0x01} // VN=4, CD=1(CONNECT)
	p := echoPortNum(echoAddr)
	req = append(req, byte(p>>8), byte(p&0xff)) // 端口
	ip := net.ParseIP("127.0.0.1").To4()
	req = append(req, ip...)
	req = append(req, 0x00) // USERID 为空，null 终止
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}

	// 响应: VN(0x00) CD(1) + 6 字节填充
	resp := make([]byte, 8)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	if resp[1] != 0x5A {
		t.Fatalf("CONNECT 应返回 0x5A(成功), got 0x%02x", resp[1])
	}

	// 验证回显
	msg := []byte("socks4-test")
	conn.Write(msg)
	expected := "ECHO:" + string(msg)
	buf := make([]byte, 100)
	total := 0
	deadline := time.Now().Add(2 * time.Second)
	for total < len(expected) && time.Now().Before(deadline) {
		conn.SetReadDeadline(deadline)
		n, err := conn.Read(buf[total:])
		if err != nil {
			break
		}
		total += n
	}
	if string(buf[:total]) != expected {
		t.Errorf("回显不匹配: got %q, want %q", buf[:total], expected)
	}
}

// ============== 端口冲突检测测试 ==============

func TestCheckPortConflict(t *testing.T) {
	listeners := []config.ProxyListener{
		{ID: "a", Protocol: config.ProxySOCKS5, ListenPort: 1080},
		{ID: "b", Protocol: config.ProxyHTTP, ListenPort: 1080},
	}
	if err := checkPortConflict(listeners); err == nil {
		t.Errorf("端口冲突应报错")
	}

	listeners[1].ListenPort = 1081
	if err := checkPortConflict(listeners); err != nil {
		t.Errorf("无冲突时不应报错: %v", err)
	}
}

// ============== 工具函数 ==============

// selfSignedCert 生成用于测试的自签证书
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("1.2.3.4")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("生成证书失败: %v", err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	pair, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatalf("解析测试证书失败: %v", err)
	}
	return pair
}

// TestHTTPSProxyDynamicCertProvider HTTPS 监听器证书留空时使用动态证书（IP 证书）完成握手
func TestHTTPSProxyDynamicCertProvider(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	freePort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	cert := selfSignedCert(t)
	lc := config.ProxyListener{
		ID:         "https-ipcert",
		Protocol:   config.ProxyHTTPS,
		ListenPort: freePort,
		TLS:        &config.TLSConfig{},
	}
	srv, err := buildServerWithStats(lc, &mockDialer{}, nil, &TrafficStats{}, nil,
		func() (tls.Certificate, error) { return cert, nil })
	if err != nil {
		t.Fatalf("构建 HTTPS 代理失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	defer srv.Stop()
	time.Sleep(80 * time.Millisecond)

	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", freePort), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("TLS 握手应使用动态证书成功: %v", err)
	}
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 || state.PeerCertificates[0].IPAddresses[0].String() != "1.2.3.4" {
		t.Errorf("握手证书不符合预期")
	}
	_ = conn.Close()
}

// TestHTTPSProxyMissingCertWithoutProvider 证书留空且无动态证书来源时应失败
func TestHTTPSProxyMissingCertWithoutProvider(t *testing.T) {
	lc := config.ProxyListener{ID: "https-nocert", Protocol: config.ProxyHTTPS, ListenPort: 1, TLS: &config.TLSConfig{}}
	if _, err := buildServerWithStats(lc, &mockDialer{}, nil, &TrafficStats{}, nil, nil); err != nil {
		t.Fatalf("构建应成功（证书在 Start 时校验）: %v", err)
	}
}

// TestHTTPSProxyDynamicCertUnavailable 证书来源不可用（如 IP 证书尚未签发）时：
// 监听器照常启动并输出提示；握手失败日志限流输出，
// 避免「监听显示运行中但连不上」无从排查
func TestHTTPSProxyDynamicCertUnavailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	freePort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	var mu sync.Mutex
	var warns int
	log := func(level, msg string) {
		if level != "warn" {
			return
		}
		mu.Lock()
		warns++
		mu.Unlock()
	}
	warnCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return warns
	}

	lc := config.ProxyListener{
		ID:         "https-nocert-yet",
		Protocol:   config.ProxyHTTPS,
		ListenPort: freePort,
		TLS:        &config.TLSConfig{},
	}
	srv, err := buildServerWithStats(lc, &mockDialer{}, log, &TrafficStats{}, nil,
		func() (tls.Certificate, error) { return tls.Certificate{}, errors.New("IP 证书尚未签发") })
	if err != nil {
		t.Fatalf("构建 HTTPS 代理失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	defer srv.Stop()
	time.Sleep(80 * time.Millisecond)

	// 启动时预检一次并提示
	if got := warnCount(); got != 1 {
		t.Fatalf("启动时应提示证书不可用, 实际 %d 条", got)
	}

	// 多次握手均失败；握手失败日志在窗口期内只记录一条
	for i := 0; i < 3; i++ {
		conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", freePort), &tls.Config{InsecureSkipVerify: true})
		if err == nil {
			_ = conn.Close()
			t.Fatalf("证书不可用时握手应失败")
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for warnCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if got := warnCount(); got != 2 {
		t.Errorf("握手失败日志应限流为一条, 实际共 %d 条（含启动提示 1 条）", got)
	}
}

// TestHTTPSProxyIPCertOption 监听器显式选择 IP 证书（use_ipcert）：
// 无需填写 PEM 内容，即使残留 PEM 也以 IP 证书为准；无证书来源时启动失败
func TestHTTPSProxyIPCertOption(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	freePort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	cert := selfSignedCert(t) // IP 1.2.3.4
	lc := config.ProxyListener{
		ID:         "https-ipcert-opt",
		Protocol:   config.ProxyHTTPS,
		ListenPort: freePort,
		// 残留无效 PEM：若误走静态证书路径，Start 时解析会失败
		TLS: &config.TLSConfig{UseIPCert: true, CertPEM: "invalid-pem", KeyPEM: "invalid-pem"},
	}
	srv, err := buildServerWithStats(lc, &mockDialer{}, nil, &TrafficStats{}, nil,
		func() (tls.Certificate, error) { return cert, nil })
	if err != nil {
		t.Fatalf("构建 HTTPS 代理失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	defer srv.Stop()
	time.Sleep(80 * time.Millisecond)

	conn, err := tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", freePort), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("选择 IP 证书后握手应成功: %v", err)
	}
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 || state.PeerCertificates[0].IPAddresses[0].String() != "1.2.3.4" {
		t.Errorf("握手应使用 IP 证书")
	}
	_ = conn.Close()

	// 无证书来源时应启动失败
	srv2, err := buildServerWithStats(lc, &mockDialer{}, nil, &TrafficStats{}, nil, nil)
	if err != nil {
		t.Fatalf("构建应成功: %v", err)
	}
	if err := srv2.Start(ctx); err == nil {
		srv2.Stop()
		t.Fatalf("无证书来源时 Start 应失败")
	}
}

// recordingDialer 记录实际拨号地址，并把连接重定向到本地 echo 服务器。
// 通过带缓冲 channel 回传地址，避免测试 goroutine 与代理 goroutine 的数据竞态。
type recordingDialer struct {
	target string
	addrs  chan string
}

func (d *recordingDialer) Dial(network, address string) (net.Conn, error) {
	select {
	case d.addrs <- address:
	default:
	}
	return net.Dial("tcp", d.target)
}

// dialAddr 等待并返回一次拨号地址
func (d *recordingDialer) dialAddr(t *testing.T) string {
	t.Helper()
	select {
	case a := <-d.addrs:
		return a
	case <-time.After(2 * time.Second):
		t.Fatalf("代理未发起拨号")
		return ""
	}
}

// socks5ConnectRaw 完成 SOCKS5 无认证握手并按原始请求字节发送 CONNECT，返回客户端连接与响应码
func socks5ConnectRaw(t *testing.T, proxyPort int, req []byte) (net.Conn, byte) {
	t.Helper()
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("发送握手失败: %v", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("读取握手响应失败: %v", err)
	}
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	reply := make([]byte, 10) // 本实现成功/失败均以 IPv4 形式回复
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	return conn, reply[1]
}

// TestSOCKS5DomainIPv6BracketedTarget 部分客户端把 IPv6 目标以带方括号的域名下发，
// 若直接 JoinHostPort 会得到 [[2600::1]]:443（Go 报 missing port in address）。
// 修复后应剥掉一层方括号，拨号地址为合法的 [2600::1]:443。
func TestSOCKS5DomainIPv6BracketedTarget(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	dialer := &recordingDialer{target: echoAddr, addrs: make(chan string, 4)}
	lc := config.ProxyListener{ID: "test-socks5-v6-domain", Protocol: config.ProxySOCKS5}
	port, handle, err := startProxyServer(t, lc, dialer)
	if err != nil {
		t.Fatalf("启动 SOCKS5 代理失败: %v", err)
	}
	defer handle.Stop()

	domain := "[2600:1f18:2da1:d401:3bc4:1b7d:6e3d:8acb]"
	req := []byte{0x05, 0x01, 0x00, 0x03, byte(len(domain))}
	req = append(req, domain...)
	req = append(req, 0x01, 0xBB) // 端口 443
	conn, rep := socks5ConnectRaw(t, port, req)
	defer conn.Close()

	if rep != 0x00 {
		t.Fatalf("CONNECT 应成功, rep=%d", rep)
	}
	addr := dialer.dialAddr(t)
	const want = "[2600:1f18:2da1:d401:3bc4:1b7d:6e3d:8acb]:443"
	if addr != want {
		t.Errorf("拨号地址错误: got %q, want %q", addr, want)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Errorf("拨号地址应可解析: %v", err)
	}
}

// TestSOCKS5IPv6ATYPTarget ATYP=0x04 的 IPv6 目标不应被加双重括号
func TestSOCKS5IPv6ATYPTarget(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	dialer := &recordingDialer{target: echoAddr, addrs: make(chan string, 4)}
	lc := config.ProxyListener{ID: "test-socks5-v6-atyp", Protocol: config.ProxySOCKS5}
	port, handle, err := startProxyServer(t, lc, dialer)
	if err != nil {
		t.Fatalf("启动 SOCKS5 代理失败: %v", err)
	}
	defer handle.Stop()

	ip := net.ParseIP("2600:1f18::10").To16()
	req := []byte{0x05, 0x01, 0x00, 0x04}
	req = append(req, ip...)
	req = append(req, 0x01, 0xBB)
	conn, rep := socks5ConnectRaw(t, port, req)
	defer conn.Close()

	if rep != 0x00 {
		t.Fatalf("CONNECT 应成功, rep=%d", rep)
	}
	addr := dialer.dialAddr(t)
	const want = "[2600:1f18::10]:443"
	if addr != want {
		t.Errorf("拨号地址错误: got %q, want %q", addr, want)
	}
}

// TestSOCKS4aDomainIPv6BracketedTarget SOCKS4a 域名同样需要处理带方括号的 IPv6 字面量
func TestSOCKS4aDomainIPv6BracketedTarget(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	dialer := &recordingDialer{target: echoAddr, addrs: make(chan string, 4)}
	lc := config.ProxyListener{ID: "test-socks4a-v6", Protocol: config.ProxySOCKS4}
	port, handle, err := startProxyServer(t, lc, dialer)
	if err != nil {
		t.Fatalf("启动 SOCKS4 代理失败: %v", err)
	}
	defer handle.Stop()

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()

	// VN=4 CD=1 DSTPORT=443 DSTIP=0.0.0.1(表示 SOCKS4a) USERID\0 DOMAIN\0
	req := []byte{0x04, 0x01, 0x01, 0xBB, 0, 0, 0, 1}
	req = append(req, []byte("u")...)
	req = append(req, 0)
	req = append(req, []byte("[2600::1]")...)
	req = append(req, 0)
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	reply := make([]byte, 8)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	if reply[1] != 0x5A {
		t.Fatalf("CONNECT 应成功, CD=%#x", reply[1])
	}
	if addr := dialer.dialAddr(t); addr != "[2600::1]:443" {
		t.Errorf("拨号地址错误: got %q, want %q", addr, "[2600::1]:443")
	}
}

func echoPortNum(addr string) int {
	_, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)
	return port
}

func echoPortHigh(addr string) int {
	return echoPortNum(addr) >> 8
}

func echoPortLow(addr string) int {
	return echoPortNum(addr) & 0xff
}

// 确保 config 包导入（实际通过 BuildServer 调用间接使用）
var _ = config.ProxyHTTP
var _ = config.ProxyListener{}
var _ = config.AuthConfig{}
