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

// waitForCond 轮询等待条件成立
func waitForCond(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// TestConnRegistryLifecycle 连接注册表的注册、注销与按隧道清理
func TestConnRegistryLifecycle(t *testing.T) {
	r := NewConnRegistry()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	e1 := r.Open(a, ConnMeta{
		TunnelID: "t1", TunnelName: "隧道一", ListenerID: "l1",
		Protocol: "socks5", ListenPort: 1080, Target: "example.com:80",
	})
	e2 := r.Open(a, ConnMeta{
		TunnelID: "t2", TunnelName: "隧道二", ListenerID: "l2",
		Protocol: "http", ListenPort: 8080, Target: "example.org:443",
	})
	e1.AddSent(11)
	e1.AddRecv(22)

	snaps := r.Snapshot()
	if len(snaps) != 2 {
		t.Fatalf("活动连接数 = %d, want 2", len(snaps))
	}
	byID := make(map[string]ConnSnapshot, len(snaps))
	for _, s := range snaps {
		byID[s.ID] = s
	}
	s1, ok := byID[e1.id]
	if !ok {
		t.Fatalf("未找到连接 %s: %+v", e1.id, snaps)
	}
	if s1.Target != "example.com:80" || s1.TunnelName != "隧道一" || s1.Protocol != "socks5" {
		t.Errorf("连接快照内容不符: %+v", s1)
	}
	if s1.BytesSent != 11 || s1.BytesRecv != 22 {
		t.Errorf("连接字节计数不符: %+v", s1)
	}

	r.Close(e1)
	if got := len(r.Snapshot()); got != 1 {
		t.Errorf("注销后活动连接数 = %d, want 1", got)
	}
	r.RemoveTunnel("t2")
	if got := len(r.Snapshot()); got != 0 {
		t.Errorf("按隧道清理后活动连接数 = %d, want 0", got)
	}

	// nil 注册表应安全
	var nilReg *ConnRegistry
	nilReg.Close(e2)
	if got := len(nilReg.Snapshot()); got != 0 {
		t.Errorf("nil 注册表快照应为空，实际 %d", got)
	}
}

// TestAccessHistoryRing 访问历史环形缓冲的容量与倒序
func TestAccessHistoryRing(t *testing.T) {
	h := NewAccessHistory(2)
	base := time.Now()
	for i := 1; i <= 3; i++ {
		h.Add(AccessRecord{
			Time:   base.Add(time.Duration(i) * time.Second),
			Target: "t" + strconv.Itoa(i),
		})
	}
	recs := h.Recent(10)
	if len(recs) != 2 {
		t.Fatalf("历史条数 = %d, want 2（容量上限）", len(recs))
	}
	if recs[0].Target != "t3" || recs[1].Target != "t2" {
		t.Errorf("历史应按时间倒序: %+v", recs)
	}
	if got := h.Recent(1); len(got) != 1 || got[0].Target != "t3" {
		t.Errorf("Recent(1) 应返回最新一条: %+v", got)
	}

	var nilHist *AccessHistory
	nilHist.Add(AccessRecord{})
	if got := len(nilHist.Recent(10)); got != 0 {
		t.Errorf("nil 历史应为空，实际 %d", got)
	}
}

// TestAccessRecordDeniedAndDisabled 结构化访问历史遵循访问日志开关与拒绝去重
func TestAccessRecordDeniedAndDisabled(t *testing.T) {
	h := NewAccessHistory(10)

	// 未开启访问日志：不记录
	off := &BaseServer{id: "off", protocol: config.ProxySOCKS5, listenPort: 1080, history: h}
	off.logAccess(nil, "example.com:80", time.Now(), "成功")
	if got := len(h.Recent(10)); got != 0 {
		t.Errorf("未开启访问日志时不应写入历史，实际 %d", got)
	}

	on := &BaseServer{id: "on", protocol: config.ProxySOCKS5, listenPort: 1080, accessLog: true, history: h}
	on.logAccess(nil, "example.com:80", time.Now(), "成功")
	on.logAccessDenied(nil, "example.org:443", "认证失败")
	on.logAccessDenied(nil, "example.org:443", "认证失败") // 同一目标 10 分钟内去重
	on.logAccessDenied(nil, "example.net:443", "认证失败") // 不同目标应分别记录

	recs := h.Recent(10)
	if len(recs) != 3 {
		t.Fatalf("历史条数 = %d, want 3: %+v", len(recs), recs)
	}
	// 倒序：最新的两条是拒绝记录，分别对应不同目标
	if !recs[0].Denied || recs[0].Target != "example.net:443" {
		t.Errorf("不同目标的拒绝记录应按目标区分: %+v", recs[0])
	}
	if !recs[1].Denied || !strings.Contains(recs[1].Result, "拒绝: 认证失败") || recs[1].Target != "example.org:443" {
		t.Errorf("拒绝记录内容不符: %+v", recs[1])
	}
	if recs[2].Denied || recs[2].Result != "成功" || recs[2].Target != "example.com:80" {
		t.Errorf("成功记录内容不符: %+v", recs[2])
	}
}

// TestSOCKS5Tracking 真实 SOCKS5 会话的活动连接与访问历史采集
func TestSOCKS5Tracking(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	reg := NewConnRegistry()
	hist := NewAccessHistory(100)
	lc := config.ProxyListener{
		ID:         "track-socks5",
		Protocol:   config.ProxySOCKS5,
		ListenPort: port,
		AccessLog:  true,
	}
	srv, err := buildServerWithStats(lc, &mockDialer{target: echoAddr}, nil, &TrafficStats{}, &Tracker{
		TunnelID:   "tun-1",
		TunnelName: "测试隧道",
		Conns:      reg,
		History:    hist,
	})
	if err != nil {
		t.Fatalf("构建 SOCKS5 服务失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	defer srv.Stop()
	time.Sleep(50 * time.Millisecond)

	conn := socks5Connect(t, port, echoAddr)

	// 目标连通后应登记为活动连接
	var snap ConnSnapshot
	if !waitForCond(t, 2*time.Second, func() bool {
		st := reg.Snapshot()
		if len(st) == 1 {
			snap = st[0]
			return true
		}
		return false
	}) {
		t.Fatalf("未登记活动连接: %+v", reg.Snapshot())
	}
	if snap.Target != echoAddr || snap.TunnelID != "tun-1" || snap.TunnelName != "测试隧道" ||
		snap.ListenerID != "track-socks5" || snap.Protocol != "socks5" || snap.Source == "" {
		t.Errorf("活动连接元信息不符: %+v", snap)
	}

	// 读取 echo 前缀并完成一次往返，单连接字节应实时累计
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
	if !waitForCond(t, 2*time.Second, func() bool {
		st := reg.Snapshot()
		return len(st) == 1 && st[0].BytesSent >= 4 && st[0].BytesRecv >= 9
	}) {
		t.Errorf("单连接字节统计不符: %+v", reg.Snapshot())
	}

	// 关闭会话后应从活动连接列表移除
	_ = conn.Close()
	if !waitForCond(t, 2*time.Second, func() bool { return len(reg.Snapshot()) == 0 }) {
		t.Errorf("会话结束后活动连接未清理: %+v", reg.Snapshot())
	}

	// 访问历史应记录目标、来源与结果
	if !waitForCond(t, 2*time.Second, func() bool { return len(hist.Recent(10)) >= 1 }) {
		t.Fatalf("未写入访问历史")
	}
	rec := hist.Recent(10)[0]
	if rec.Target != echoAddr || rec.Result != "成功" || rec.Denied ||
		rec.Source == "" || rec.TunnelName != "测试隧道" || rec.Protocol != "socks5" {
		t.Errorf("访问历史内容不符: %+v", rec)
	}
}

// socks5Connect 通过 SOCKS5 代理连接 echo 服务，返回保持打开的连接
func socks5Connect(t *testing.T, proxyPort int, echoAddr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("发送握手失败: %v", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil || resp[1] != 0x00 {
		t.Fatalf("握手失败: %v %v", resp, err)
	}

	host, portStr, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatalf("解析 echo 地址失败: %v", err)
	}
	ip := net.ParseIP(host).To4()
	p, err := strconv.Atoi(portStr)
	if err != nil || ip == nil {
		t.Fatalf("echo 地址不合法: %s", echoAddr)
	}
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, ip...)
	req = append(req, byte(p>>8), byte(p))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("发送 CONNECT 失败: %v", err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != 0x00 {
		t.Fatalf("CONNECT 失败: %v %v", reply, err)
	}
	return conn
}

// TestHTTPConnectTracking HTTP CONNECT 会话的活动连接与访问历史采集
func TestHTTPConnectTracking(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	reg := NewConnRegistry()
	hist := NewAccessHistory(100)
	lc := config.ProxyListener{
		ID:         "track-http",
		Protocol:   config.ProxyHTTP,
		ListenPort: port,
		AccessLog:  true,
	}
	srv, err := buildServerWithStats(lc, &mockDialer{target: echoAddr}, nil, &TrafficStats{}, &Tracker{
		TunnelID:   "tun-http",
		TunnelName: "HTTP 隧道",
		Conns:      reg,
		History:    hist,
	})
	if err != nil {
		t.Fatalf("构建 HTTP 代理失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	defer srv.Stop()
	time.Sleep(50 * time.Millisecond)

	conn, br := httpConnect(t, port, echoAddr)

	// CONNECT 建立后应登记为活动连接
	var snap ConnSnapshot
	if !waitForCond(t, 2*time.Second, func() bool {
		st := reg.Snapshot()
		if len(st) == 1 {
			snap = st[0]
			return true
		}
		return false
	}) {
		t.Fatalf("未登记活动连接: %+v", reg.Snapshot())
	}
	if snap.Target != echoAddr || snap.Protocol != "http" || snap.ListenerID != "track-http" ||
		snap.TunnelID != "tun-http" || snap.TunnelName != "HTTP 隧道" {
		t.Errorf("活动连接元信息不符: %+v", snap)
	}

	// 读取 echo 前缀并完成一次往返，单连接字节应实时累计
	prefix := make([]byte, 5)
	if _, err := io.ReadFull(br, prefix); err != nil {
		t.Fatalf("读取下行数据失败: %v", err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("发送数据失败: %v", err)
	}
	echoed := make([]byte, 4)
	if _, err := io.ReadFull(br, echoed); err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}
	if !waitForCond(t, 2*time.Second, func() bool {
		st := reg.Snapshot()
		return len(st) == 1 && st[0].BytesSent >= 4 && st[0].BytesRecv >= 9
	}) {
		t.Errorf("单连接字节统计不符: %+v", reg.Snapshot())
	}

	_ = conn.Close()
	if !waitForCond(t, 2*time.Second, func() bool { return len(reg.Snapshot()) == 0 }) {
		t.Errorf("会话结束后活动连接未清理: %+v", reg.Snapshot())
	}
	if !waitForCond(t, 2*time.Second, func() bool { return len(hist.Recent(10)) >= 1 }) {
		t.Fatalf("未写入访问历史")
	}
	rec := hist.Recent(10)[0]
	if rec.Target != echoAddr || rec.Result != "成功" || rec.Denied || rec.Protocol != "http" {
		t.Errorf("访问历史内容不符: %+v", rec)
	}
}

// httpConnect 通过 HTTP 代理建立 CONNECT 隧道，返回连接与已缓冲的读取器
func httpConnect(t *testing.T, proxyPort int, target string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(proxyPort)))
	if err != nil {
		t.Fatalf("连接代理失败: %v", err)
	}
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("发送 CONNECT 失败: %v", err)
	}
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读取 CONNECT 响应失败: %v", err)
	}
	if !strings.Contains(line, " 200 ") {
		t.Fatalf("CONNECT 应答不符: %s", line)
	}
	// 读取剩余响应头直到空行
	for {
		l, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("读取响应头失败: %v", err)
		}
		if l == "\r\n" || l == "\n" {
			break
		}
	}
	return conn, br
}
