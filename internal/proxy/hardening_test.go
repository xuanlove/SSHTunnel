package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/config"
)

// startTestProxy 启动指定协议的测试代理，返回监听端口
func startTestProxy(t *testing.T, lc config.ProxyListener, dialer Dialer) int {
	t.Helper()
	port, _ := startTestProxyWith(t, lc, dialer, nil)
	return port
}

// startTestProxyWith 启动测试代理并允许在启动前调整参数
func startTestProxyWith(t *testing.T, lc config.ProxyListener, dialer Dialer, setup func(Server)) (int, Server) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	lc.ListenPort = port
	srv, err := BuildServer(lc, dialer, nil)
	if err != nil {
		t.Fatalf("构建代理失败: %v", err)
	}
	if setup != nil {
		setup(srv)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = srv.Stop()
	})
	go func() { _ = srv.Start(ctx) }()
	time.Sleep(50 * time.Millisecond)
	return port, srv
}

// TestSOCKS4RejectsOversizedUserID 超长 USERID 应被拒绝并断开
func TestSOCKS4RejectsOversizedUserID(t *testing.T) {
	port := startTestProxy(t, config.ProxyListener{
		ID:       "socks4-overlong",
		Protocol: config.ProxySOCKS4,
	}, &mockDialer{target: "127.0.0.1:1"})

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// VN=4, CD=1, PORT, IP
	header := []byte{0x04, 0x01, 0x00, 0x50, 127, 0, 0, 1}
	if _, err := conn.Write(header); err != nil {
		t.Fatalf("发送头部失败: %v", err)
	}
	// 超长 USERID（超过上限，且不发送结束符）
	big := make([]byte, maxSocksFieldLen+1)
	for i := range big {
		big[i] = 'a'
	}
	if _, err := conn.Write(big); err != nil {
		t.Fatalf("发送超长 USERID 失败: %v", err)
	}

	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Errorf("超长 USERID 应导致连接被关闭")
	}
}

// TestSOCKS5RejectsEmptyMethods 未提供任何认证方法应被拒绝
func TestSOCKS5RejectsEmptyMethods(t *testing.T) {
	port := startTestProxy(t, config.ProxyListener{
		ID:       "socks5-empty-methods",
		Protocol: config.ProxySOCKS5,
	}, &mockDialer{target: "127.0.0.1:1"})

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		t.Fatalf("发送握手失败: %v", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	if resp[0] != 0x05 || resp[1] != 0xFF {
		t.Errorf("应拒绝空认证方法，实际响应 %v", resp)
	}
}

// TestHTTPConnectPreservesPrefetchedBytes CONNECT 与业务数据同包发送时不得丢失
func TestHTTPConnectPreservesPrefetchedBytes(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	port := startTestProxy(t, config.ProxyListener{
		ID:       "http-prefetch",
		Protocol: config.ProxyHTTP,
	}, &mockDialer{target: echoAddr})

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// 同一次写入中发送 CONNECT 与业务数据，模拟客户端在收到 200 前即发送首包
	payload := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\nping", echoAddr, echoAddr)
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("发送 CONNECT 失败: %v", err)
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	if !strings.Contains(line, " 200 ") {
		t.Fatalf("CONNECT 应答不符: %s", line)
	}
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("读取响应头失败: %v", err)
		}
		if l == "\r\n" || l == "\n" {
			break
		}
	}

	prefix := make([]byte, 5)
	if _, err := io.ReadFull(br, prefix); err != nil {
		t.Fatalf("读取下行数据失败: %v", err)
	}
	echoed := make([]byte, 4)
	if _, err := io.ReadFull(br, echoed); err != nil {
		t.Fatalf("预读数据丢失，未收到回显: %v", err)
	}
	if string(echoed) != "ping" {
		t.Errorf("回显不符: %q", echoed)
	}
}

// TestCityRuleRequiresCityDB 城市规则在未加载城市库时必须启动失败
func TestCityRuleRequiresCityDB(t *testing.T) {
	lc := config.ProxyListener{
		ID:       "city-rule",
		Protocol: config.ProxySOCKS5,
		Firewall: &config.FirewallConfig{
			Enabled: true,
			Cities:  []string{"CN/GD/Shenzhen"},
		},
	}
	if _, err := BuildServer(lc, &mockDialer{}, nil); err == nil {
		t.Fatalf("未加载城市库时应拒绝启动城市规则监听器")
	}
}

// TestSOCKS5HandshakeTimeout 慢速客户端未完成握手时应被断开
func TestSOCKS5HandshakeTimeout(t *testing.T) {
	port, _ := startTestProxyWith(t, config.ProxyListener{
		ID:       "socks5-timeout",
		Protocol: config.ProxySOCKS5,
	}, &mockDialer{target: "127.0.0.1:1"}, func(s Server) {
		if v, ok := s.(*socks5Server); ok {
			v.hsTimeout = 100 * time.Millisecond
		}
	})

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

	// 不发送任何握手数据，读取应在超时后返回 EOF/错误
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Errorf("握手超时后连接应被关闭")
	}
}

// TestHTTPHeaderTooLarge 请求头超过限制时返回 431
func TestHTTPHeaderTooLarge(t *testing.T) {
	port, _ := startTestProxyWith(t, config.ProxyListener{
		ID:       "http-big-header",
		Protocol: config.ProxyHTTP,
	}, &mockDialer{target: "127.0.0.1:1"}, func(s Server) {
		if v, ok := s.(*httpServer); ok {
			v.maxHeader = 1024
			v.readTimeout = 2 * time.Second
		}
	})

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	big := strings.Repeat("a", 2048)
	req := fmt.Sprintf("GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\nX-Big: %s\r\n\r\n", big)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	if !strings.Contains(line, "431") {
		t.Errorf("应返回 431，实际: %s", line)
	}
}
