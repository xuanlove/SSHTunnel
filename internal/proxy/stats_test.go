package proxy

import (
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sshtunnel/internal/config"
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
		BridgeStats(aProxy, bProxy, stats)
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

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}

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

	// 读取 echo 前缀 "ECHO:"（下行 5 字节）
	prefix := make([]byte, 5)
	if _, err := io.ReadFull(conn, prefix); err != nil {
		t.Fatalf("读取下行数据失败: %v", err)
	}
	// 发送 4 字节（上行），并读回 4 字节（下行）
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("发送数据失败: %v", err)
	}
	echoed := make([]byte, 4)
	if _, err := io.ReadFull(conn, echoed); err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}
	_ = conn.Close()

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

// TestBaseServerAccessLog 访问日志开关
func TestBaseServerAccessLog(t *testing.T) {
	var logs []string
	logf := func(level, msg string) { logs = append(logs, level+":"+msg) }

	// 关闭时不应记录
	off := BaseServer{stats: &TrafficStats{}, log: logf}
	off.logAccess("example.com:80", time.Now(), "成功")
	if len(logs) != 0 {
		t.Errorf("未开启访问日志时不应记录，实际 %v", logs)
	}

	// 开启时记录目标与结果
	on := BaseServer{stats: &TrafficStats{}, log: logf, accessLog: true}
	on.logAccess("example.com:80", time.Now(), "成功")
	if len(logs) != 1 {
		t.Fatalf("应记录一条访问日志，实际 %v", logs)
	}
	if want := "访问 example.com:80"; !strings.Contains(logs[0], want) || !strings.Contains(logs[0], "成功") {
		t.Errorf("日志内容不符: %s", logs[0])
	}
}
