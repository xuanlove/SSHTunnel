package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
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
