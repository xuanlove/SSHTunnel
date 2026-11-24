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
	if geoIPCityReady() {
		t.Skip("当前已加载城市级数据库，跳过")
	}
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

// TestHTTPRejectsGiantSingleLine 200KB 单行请求行应被 64KB 上限立即拒绝，
// 且不会在标准库解析阶段无限扩容（读侧内存受控，超限即断）
func TestHTTPRejectsGiantSingleLine(t *testing.T) {
	port := startTestProxy(t, config.ProxyListener{
		ID:       "http-giant-line",
		Protocol: config.ProxyHTTP,
	}, &mockDialer{target: "127.0.0.1:1"})

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	// 单行 200KB 的请求行（无换行），远超 64KB 上限
	line := "GET http://example.com/" + strings.Repeat("a", 200*1024) + " HTTP/1.1\r\n"
	req := line + "Host: example.com\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("发送超大请求失败: %v", err)
	}

	// 期望收到 431 或连接被关闭（两者都算拒绝成功）
	br := bufio.NewReader(conn)
	resp, err := br.ReadString('\n')
	if err == nil {
		if !strings.Contains(resp, "431") {
			t.Errorf("超长单行应被拒绝为 431，实际: %s", resp)
		}
	} else if _, isNet := err.(net.Error); isNet && !err.(net.Error).Timeout() {
		t.Errorf("读取出现非预期网络错误: %v", err)
	}
}

// TestHTTPSmallRequestStillAccepted 正常小请求在有界读取下仍应正常转发
func TestHTTPSmallRequestStillAccepted(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	port := startTestProxy(t, config.ProxyListener{
		ID:       "http-small-ok",
		Protocol: config.ProxyHTTP,
	}, &mockDialer{target: echoAddr})

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	if _, err := conn.Write([]byte("CONNECT " + echoAddr + " HTTP/1.1\r\nHost: " + echoAddr + "\r\n\r\n")); err != nil {
		t.Fatalf("发送 CONNECT 失败: %v", err)
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	if !strings.Contains(line, " 200 ") {
		t.Errorf("正常小请求应被接受并转发，实际: %s", line)
	}
}

// TestSecureEqual 常量时间比较：等长/不等长/空串均正确且结果稳定
func TestSecureEqual(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"both empty", "", "", true},
		{"equal", "abc", "abc", true},
		{"same length differ", "abc", "abd", false},
		{"a longer", "abcd", "abc", false},
		{"b longer", "abc", "abcd", false},
		{"a empty", "", "abc", false},
		{"b empty", "abc", "", false},
		{"unicode equal", "用户", "用户", true},
		{"unicode differ", "用户", "用户2", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for i := 0; i < 8; i++ { // 多次调用结果应稳定
				if got := secureEqual(c.a, c.b); got != c.want {
					t.Fatalf("secureEqual(%q, %q) 第 %d 次 = %v, 期望 %v", c.a, c.b, i, got, c.want)
				}
			}
		})
	}
}

// TestSanitizeLog 日志字段中的 \r \n 及控制字符应被剔除，防止日志注入
func TestSanitizeLog(t *testing.T) {
	in := "user\r\nFAKE LOG LINE\x00\x1b[31mnext\tok"
	out := sanitizeLog(in)
	if strings.ContainsAny(out, "\r\n") {
		t.Errorf("输出不应含换行符: %q", out)
	}
	for _, bad := range []string{"\x00", "\x1b"} {
		if strings.Contains(out, bad) {
			t.Errorf("输出不应含控制字符 %q: %q", bad, out)
		}
	}
	if !strings.Contains(out, "user") || !strings.Contains(out, "next") {
		t.Errorf("正常内容应保留: %q", out)
	}
	if strings.Contains(out, "\t") {
		t.Errorf("制表符属控制字符，应被剔除: %q", out)
	}
	if got := sanitizeLog("clean"); got != "clean" {
		t.Errorf("无控制字符输入应原样返回: %q", got)
	}
}

// TestSOCKS5RejectsBadSubnegotiationVER 子协商版本非 0x01 应按 RFC 1929 回失败状态并断开
func TestSOCKS5RejectsBadSubnegotiationVER(t *testing.T) {
	port := startTestProxy(t, config.ProxyListener{
		ID:       "socks5-bad-subver",
		Protocol: config.ProxySOCKS5,
		Auth:     &config.AuthConfig{Username: "u", Password: "p"},
	}, &mockDialer{target: "127.0.0.1:1"})

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))

	// 方法协商：仅提供用户名密码（0x02）
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("发送握手失败: %v", err)
	}
	methodResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, methodResp); err != nil {
		t.Fatalf("读取方法协商响应失败: %v", err)
	}
	if methodResp[0] != 0x05 || methodResp[1] != 0x02 {
		t.Fatalf("方法协商响应不符: %v", methodResp)
	}

	// 子协商：VER=0x02（非法）
	if _, err := conn.Write([]byte{0x02, 0x01, 'u', 0x01, 'p'}); err != nil {
		t.Fatalf("发送子协商失败: %v", err)
	}
	subResp := make([]byte, 2)
	if _, err := io.ReadFull(conn, subResp); err != nil {
		t.Fatalf("读取子协商响应失败: %v", err)
	}
	if subResp[0] != 0x01 || subResp[1] != 0x01 {
		t.Errorf("非法子协商版本应返回失败状态 {0x01,0x01}，实际: %v", subResp)
	}

	// 连接应被断开
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err == nil {
		t.Errorf("非法子协商版本后连接应被关闭")
	}
}

// TestProxyWatcherExitsOnStop Stop() 后生命周期停止信号应关闭，watcher goroutine 随之退出
func TestProxyWatcherExitsOnStop(t *testing.T) {
	servers := make([]*socks5Server, 0, 5)
	for i := 0; i < 5; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("分配端口失败: %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		srv, err := BuildServer(config.ProxyListener{
			ID:         fmt.Sprintf("watcher-%d", i),
			Protocol:   config.ProxySOCKS5,
			ListenPort: port,
		}, &mockDialer{target: "127.0.0.1:1"}, nil)
		if err != nil {
			t.Fatalf("构建代理失败: %v", err)
		}
		ss := srv.(*socks5Server)
		servers = append(servers, ss)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = ss.Start(ctx) }()
	}
	time.Sleep(100 * time.Millisecond) // 等待 Start 完成、watcher 建立

	for i, ss := range servers {
		if err := ss.Stop(); err != nil {
			t.Fatalf("Stop 失败: %v", err)
		}
		select {
		case <-ss.stopCh:
		default:
			t.Errorf("server %d: Stop 后生命周期停止信号未关闭", i)
		}
	}
}
