package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
)

// TestTrafficStatsCounters 计数与快照
func TestTrafficStatsCounters(t *testing.T) {
	s := &TrafficStats{}
	s.BeginConn()
	s.BeginConn()
	s.AddSent(100)
	s.AddRecv(200)
	s.AddSent(50)
	s.EndConn()

	active, total, sent, recv := s.Snapshot()
	if active != 1 || total != 2 || sent != 150 || recv != 200 {
		t.Errorf("统计不符: active=%d total=%d sent=%d recv=%d", active, total, sent, recv)
	}
}

// TestBridgeStats 双向桥接按方向统计上下行字节
func TestBridgeStats(t *testing.T) {
	// a 为客户端侧，b 为目标侧
	aClient, aProxy := net.Pipe()
	bProxy, bTarget := net.Pipe()
	defer aClient.Close()
	defer bTarget.Close()

	stats := &TrafficStats{}
	stats.BeginConn()
	done := make(chan struct{})
	go func() {
		BridgeStats(aProxy, bProxy, stats, nil)
		close(done)
	}()

	// 客户端 -> 目标（上行 5 字节）
	go func() { _, _ = aClient.Write([]byte("hello")) }()
	up := make([]byte, 5)
	if _, err := io.ReadFull(bTarget, up); err != nil {
		t.Fatalf("目标读取失败: %v", err)
	}

	// 目标 -> 客户端（下行 6 字节）
	go func() { _, _ = bTarget.Write([]byte("world!")) }()
	down := make([]byte, 6)
	if _, err := io.ReadFull(aClient, down); err != nil {
		t.Fatalf("客户端读取失败: %v", err)
	}

	// 关闭连接结束桥接
	_ = aClient.Close()
	_ = bTarget.Close()
	<-done
	stats.EndConn()

	// 计数在写入时累计，等待桥接协程完成写入统计
	var active, total, sent, recv int64
	deadline := time.Now().Add(2 * time.Second)
	for {
		active, total, sent, recv = stats.Snapshot()
		if (sent == 5 && recv == 6) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if active != 0 || total != 1 {
		t.Errorf("连接计数不符: active=%d total=%d", active, total)
	}
	if sent != 5 {
		t.Errorf("上行字节 = %d, want 5", sent)
	}
	if recv != 6 {
		t.Errorf("下行字节 = %d, want 6", recv)
	}
}

// TestSOCKS5StatsAndAccessLog 真实 SOCKS5 会话的连接/流量统计与访问日志
func TestSOCKS5StatsAndAccessLog(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	var mu sync.Mutex
	var logs []string
	lc := config.ProxyListener{
		ID:         "stats-socks5",
		Protocol:   config.ProxySOCKS5,
		ListenPort: 0,
		AccessLog:  true,
	}
	// 选一个可用端口
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	lc.ListenPort = port

	srv, err := BuildServer(lc, &mockDialer{target: echoAddr}, func(level, msg string) {
		mu.Lock()
		logs = append(logs, level+" "+msg)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("构建 SOCKS5 服务失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	defer srv.Stop()
	time.Sleep(50 * time.Millisecond)

	doSOCKS5EchoSession(t, port, echoAddr)

	// 等待连接结束并核对统计
	deadline := time.Now().Add(2 * time.Second)
	var st ListenerStats
	for {
		st = srv.Stats()
		if (st.ActiveConns == 0 && st.TotalConns == 1 && st.BytesSent >= 4 && st.BytesRecv >= 9) ||
			time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if st.TotalConns != 1 || st.ActiveConns != 0 {
		t.Errorf("连接统计不符: %+v", st)
	}
	if st.BytesSent != 4 {
		t.Errorf("上行字节 = %d, want 4", st.BytesSent)
	}
	if st.BytesRecv != 9 {
		t.Errorf("下行字节 = %d, want 9", st.BytesRecv)
	}
	if st.ID != "stats-socks5" || st.ListenPort != port || st.Protocol != "socks5" {
		t.Errorf("监听器标识不符: %+v", st)
	}

	// 访问日志应记录目标与结果
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, l := range logs {
		if strings.Contains(l, "访问") && strings.Contains(l, "成功") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("未找到访问日志: %v", logs)
	}
}

// doSOCKS5EchoSession 通过 SOCKS5 代理与 echo 服务器完成一次握手与数据往返
func doSOCKS5EchoSession(t *testing.T, proxyPort int, echoAddr string) {
	t.Helper()
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()

	// SOCKS5 握手（无认证）
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("发送握手失败: %v", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil || resp[1] != 0x00 {
		t.Fatalf("握手失败: %v %v", resp, err)
	}
	// CONNECT 到 echo 服务器
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, net.ParseIP("127.0.0.1").To4()...)
	req = append(req, byte(echoPortHigh(echoAddr)), byte(echoPortLow(echoAddr)))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("发送 CONNECT 失败: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0x00 {
		t.Fatalf("CONNECT 失败: %v %v", reply, err)
	}
	// 读取 echo 前缀并完成一次往返
	prefix := make([]byte, 5)
	if _, err := io.ReadFull(conn, prefix); err != nil {
		t.Fatalf("读取下行数据失败: %v", err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("发送数据失败: %v", err)
	}
	echoed := make([]byte, 4)
	if _, err := io.ReadFull(conn, echoed); err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}
}

// TestManagerStatsPersistAfterRestart 隧道重连重建监听器后，累计统计不应清零
func TestManagerStatsPersistAfterRestart(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	listeners := []config.ProxyListener{{
		ID:            "persist-stats",
		Protocol:      config.ProxySOCKS5,
		ListenPort:    port,
		AllowExternal: false,
	}}
	m := NewManager(&mockDialer{target: echoAddr}, logger.NewTest(), "persist", "持久统计")
	ctx := context.Background()
	if err := m.StartAll(ctx, listeners); err != nil {
		t.Fatalf("首次启动失败: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	doSOCKS5EchoSession(t, port, echoAddr)

	// 等待首次会话统计落账
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := m.Stats()
		if (len(st) == 1 && st[0].ActiveConns == 0 && st[0].TotalConns == 1) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// 模拟隧道重连：停止并重新启动监听器
	m.StopAll()

	// 停止期间（重连窗口）仍应输出累计统计，避免界面归零
	stStopped := m.Stats()
	if len(stStopped) != 1 {
		t.Fatalf("重连窗口应保留监听器统计，实际 %d", len(stStopped))
	}
	if stStopped[0].TotalConns != 1 || stStopped[0].BytesSent != 4 || stStopped[0].BytesRecv != 9 {
		t.Errorf("重连窗口累计统计应保留: %+v", stStopped[0])
	}

	if err := m.StartAll(ctx, listeners); err != nil {
		t.Fatalf("重新启动失败: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	st := m.Stats()
	if len(st) != 1 {
		t.Fatalf("应返回 1 个监听器统计，实际 %d", len(st))
	}
	if st[0].TotalConns != 1 {
		t.Errorf("重连后累计连接数应保留为 1，实际 %d", st[0].TotalConns)
	}
	if st[0].BytesSent != 4 || st[0].BytesRecv != 9 {
		t.Errorf("重连后累计流量应保留，实际 sent=%d recv=%d", st[0].BytesSent, st[0].BytesRecv)
	}
	m.StopAll()
}

// TestHTTPProxyPayloadStats HTTP 代理仅统计请求/响应体字节（不含协议头）
func TestHTTPProxyPayloadStats(t *testing.T) {
	const reqBody = "PING"
	const respBody = "hello-body" // 10 字节

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = io.WriteString(w, respBody)
	}))
	defer target.Close()
	targetAddr := strings.TrimPrefix(target.URL, "http://")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	lc := config.ProxyListener{
		ID:         "http-stats",
		Protocol:   config.ProxyHTTP,
		ListenPort: port,
	}
	srv, err := BuildServer(lc, &mockDialer{target: targetAddr}, nil)
	if err != nil {
		t.Fatalf("构建 HTTP 代理失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	defer srv.Stop()
	time.Sleep(50 * time.Millisecond)

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	req := fmt.Sprintf("POST http://%s/ HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		targetAddr, targetAddr, len(reqBody), reqBody)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}

	// 读取响应并核对内容
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	if string(body) != respBody {
		t.Errorf("响应体 = %q, want %q", body, respBody)
	}
	_ = conn.Close()

	// 统计应只包含 body 字节（4 上行 / 10 下行）
	deadline := time.Now().Add(2 * time.Second)
	var st ListenerStats
	for {
		st = srv.Stats()
		if (st.ActiveConns == 0 && st.BytesSent == int64(len(reqBody)) && st.BytesRecv == int64(len(respBody))) ||
			time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if st.BytesSent != int64(len(reqBody)) {
		t.Errorf("上行字节 = %d, want %d（仅请求体）", st.BytesSent, len(reqBody))
	}
	if st.BytesRecv != int64(len(respBody)) {
		t.Errorf("下行字节 = %d, want %d（仅响应体）", st.BytesRecv, len(respBody))
	}
}

// TestAccessLogDenied 防火墙/认证拒绝的访问日志（按来源与原因去重）
func TestAccessLogDenied(t *testing.T) {
	var logs []string
	b := &BaseServer{
		stats:     &TrafficStats{},
		accessLog: true,
		log:       func(level, msg string) { logs = append(logs, level+" "+msg) },
	}
	a, c := net.Pipe()
	defer a.Close()
	defer c.Close()

	b.logAccessDenied(a, "", "认证失败")
	b.logAccessDenied(a, "", "认证失败") // 10 分钟内重复拒绝不应重复记录

	if len(logs) != 1 {
		t.Fatalf("应只记录一条拒绝日志，实际 %v", logs)
	}
	if !strings.Contains(logs[0], "拒绝: 认证失败") {
		t.Errorf("日志内容不符: %s", logs[0])
	}

	// 目标已知时应同时输出目标地址（如 HTTP 认证失败）
	var withTarget []string
	bt := &BaseServer{
		stats:     &TrafficStats{},
		accessLog: true,
		log:       func(level, msg string) { withTarget = append(withTarget, msg) },
	}
	bt.logAccessDenied(a, "example.com:443", "认证失败")
	if len(withTarget) != 1 {
		t.Fatalf("应记录一条带目标的拒绝日志，实际 %v", withTarget)
	}
	if !strings.Contains(withTarget[0], "访问 example.com:443") || !strings.Contains(withTarget[0], "拒绝: 认证失败") {
		t.Errorf("带目标的拒绝日志内容不符: %s", withTarget[0])
	}

	// 未开启访问日志时不记录
	off := &BaseServer{stats: &TrafficStats{}, log: func(level, msg string) { logs = append(logs, msg) }}
	off.logAccessDenied(a, "", "防火墙规则")
	if len(logs) != 1 {
		t.Errorf("未开启访问日志时不应记录，实际 %v", logs)
	}
}

// TestHTTPAuthFailureLogsTarget HTTP 认证失败的访问日志应包含已解析出的目标地址
func TestHTTPAuthFailureLogsTarget(t *testing.T) {
	var mu sync.Mutex
	var logs []string

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	lc := config.ProxyListener{
		ID:         "http-auth-log",
		Protocol:   config.ProxyHTTP,
		ListenPort: port,
		AccessLog:  true,
		Auth:       &config.AuthConfig{Username: "u", Password: "p"},
	}
	srv, err := BuildServer(lc, &mockDialer{target: "127.0.0.1:1"}, func(level, msg string) {
		mu.Lock()
		logs = append(logs, msg)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("构建 HTTP 代理失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	defer srv.Stop()
	time.Sleep(50 * time.Millisecond)

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	defer conn.Close()
	req := "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("应返回 407，实际 %d", resp.StatusCode)
	}

	// 等待认证失败日志落账
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		found := false
		for _, l := range logs {
			if strings.Contains(l, "访问 example.com") && strings.Contains(l, "拒绝: 认证失败") {
				found = true
				break
			}
		}
		mu.Unlock()
		if found || time.Now().After(deadline) {
			if !found {
				mu.Lock()
				t.Errorf("未找到包含目标的认证失败日志: %v", logs)
				mu.Unlock()
			}
			break
		}
		time.Sleep(time.Millisecond)
	}
}

// TestBaseServerStatsNilSafe 未初始化统计对象时 Stats 不应 panic
func TestBaseServerStatsNilSafe(t *testing.T) {
	b := &BaseServer{id: "nil-stats", protocol: config.ProxySOCKS5, listenPort: 1080}
	st := b.Stats()
	if st.ID != "nil-stats" || st.TotalConns != 0 || st.BytesSent != 0 {
		t.Errorf("统计结果不符: %+v", st)
	}
}

// TestBaseServerAccessLog 访问日志开关
func TestBaseServerAccessLog(t *testing.T) {
	var logs []string
	logf := func(level, msg string) { logs = append(logs, level+":"+msg) }

	// 关闭时不应记录
	off := BaseServer{stats: &TrafficStats{}, log: logf}
	off.logAccess(nil, "example.com:80", time.Now(), "成功")
	if len(logs) != 0 {
		t.Errorf("未开启访问日志时不应记录，实际 %v", logs)
	}

	// 开启时记录目标与结果
	on := BaseServer{stats: &TrafficStats{}, log: logf, accessLog: true}
	on.logAccess(nil, "example.com:80", time.Now(), "成功")
	if len(logs) != 1 {
		t.Fatalf("应记录一条访问日志，实际 %v", logs)
	}
	if want := "访问 example.com:80"; !strings.Contains(logs[0], want) || !strings.Contains(logs[0], "成功") {
		t.Errorf("日志内容不符: %s", logs[0])
	}
}
