package tunnel

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/proxy"
)

// startLocalEcho 启动本地回显服务器
func startLocalEcho(t *testing.T) (net.Listener, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动回显服务器失败: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
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
			}(c)
		}
	}()
	return ln, func() { _ = ln.Close() }
}

// freeLocalPort 分配一个当前空闲的本地端口
func freeLocalPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配端口失败: %v", err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

// echoSession 完成一次本地转发的数据往返
func echoSession(t *testing.T, addr string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("连接本地转发失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("发送数据失败: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("回显不符: %q", buf)
	}
}

// TestStatsArchivedAcrossStop 隧道停止后应保留累计统计，再次启动后继续累加
func TestStatsArchivedAcrossStop(t *testing.T) {
	echoLn, stopEcho := startLocalEcho(t)
	defer stopEcho()

	localPort := freeLocalPort(t)
	d := &mockDialer{}
	d.dialFn = func(network, address string) (net.Conn, error) {
		return net.Dial("tcp", echoLn.Addr().String())
	}
	withMockDialer(t, d)

	m := newTestManager(t)
	cfg := &config.TunnelConfig{
		ID:            "archived",
		Name:          "归档统计",
		HopChain:      []config.HopConfig{{User: "u", Host: "h", Port: 22, AuthType: "password", Password: "p"}},
		TunnelType:    config.TunnelLocalForward,
		AutoReconnect: false,
		LocalForwards: []config.LocalForward{
			{ID: "lf-1", LocalPort: localPort, RemoteHost: "127.0.0.1", RemotePort: 80},
		},
	}
	addr := fmt.Sprintf("127.0.0.1:%d", localPort)

	if err := m.Start(cfg); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	echoSession(t, addr)

	// 等待首次会话统计落账
	if !waitFor(2*time.Second, func() bool {
		st := m.Stats()["archived"]
		return len(st.Forwards) == 1 && st.Forwards[0].TotalConns == 1 && st.Forwards[0].BytesSent == 4
	}) {
		t.Fatalf("首次统计未落账: %+v", m.Stats()["archived"])
	}

	if err := m.Stop("archived"); err != nil {
		t.Fatalf("停止失败: %v", err)
	}

	// 停止后累计值保留，活动连接与明细清空
	st := m.Stats()["archived"]
	if st.TotalConns != 1 || st.BytesSent != 4 || st.BytesRecv != 4 {
		t.Errorf("停止后累计统计应保留: %+v", st)
	}
	if st.ActiveConns != 0 || len(st.Forwards) != 0 {
		t.Errorf("停止后活动连接与明细应为空: %+v", st)
	}

	// 再次启动并产生一次会话，累计值应继续增长
	if err := m.Start(cfg); err != nil {
		t.Fatalf("重新启动失败: %v", err)
	}
	defer m.Stop("archived")
	echoSession(t, addr)

	if !waitFor(2*time.Second, func() bool {
		st := m.Stats()["archived"]
		return st.TotalConns == 2 && st.BytesSent == 8 && st.BytesRecv == 8
	}) {
		t.Errorf("重启后累计统计不符: %+v", m.Stats()["archived"])
	}
}

// TestNodeStatusConnectionsAndArchive 节点状态应包含活动连接，停止后保留累计流量
func TestNodeStatusConnectionsAndArchive(t *testing.T) {
	echoLn, stopEcho := startLocalEcho(t)
	defer stopEcho()

	localPort := freeLocalPort(t)
	d := &mockDialer{}
	d.dialFn = func(network, address string) (net.Conn, error) {
		return net.Dial("tcp", echoLn.Addr().String())
	}
	withMockDialer(t, d)

	m := newTestManager(t)
	cfg := &config.TunnelConfig{
		ID:            "node-status",
		Name:          "节点状态测试",
		HopChain:      []config.HopConfig{{User: "u", Host: "h", Port: 22, AuthType: "password", Password: "p"}},
		TunnelType:    config.TunnelLocalForward,
		AutoReconnect: false,
		LocalForwards: []config.LocalForward{
			{ID: "lf-1", LocalPort: localPort, RemoteHost: "127.0.0.1", RemotePort: 80},
		},
	}
	if err := m.Start(cfg); err != nil {
		t.Fatalf("启动失败: %v", err)
	}

	// 保持一条连接打开，节点状态应展示该活动连接
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		t.Fatalf("连接本地转发失败: %v", err)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("发送数据失败: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("读取回显失败: %v", err)
	}

	cfgs := []config.TunnelConfig{*cfg}
	var status NodeStatus
	if !waitFor(2*time.Second, func() bool {
		status = m.NodeStatus(cfgs, 50)
		return len(status.Connections) == 1
	}) {
		t.Fatalf("节点状态应包含 1 条活动连接: %+v", status.Connections)
	}
	if status.TunnelTotal != 1 || status.TunnelRunning != 1 || status.ForwardCount != 1 {
		t.Errorf("节点概览不符: %+v", status)
	}
	if status.ActiveConns < 1 {
		t.Errorf("活动连接数应 >= 1，实际 %d", status.ActiveConns)
	}
	c := status.Connections[0]
	if c.Protocol != "local" || c.TunnelID != cfg.ID || c.TunnelName != cfg.Name ||
		c.ListenerID != "lf-1" || c.ListenPort != localPort || c.Target != "127.0.0.1:80" {
		t.Errorf("活动连接内容不符: %+v", c)
	}
	if !waitFor(2*time.Second, func() bool {
		status = m.NodeStatus(cfgs, 50)
		return len(status.Connections) == 1 && status.Connections[0].BytesSent >= 4
	}) {
		t.Errorf("活动连接字节未实时累计: %+v", status.Connections)
	}

	_ = conn.Close()
	if !waitFor(2*time.Second, func() bool {
		return len(m.NodeStatus(cfgs, 50).Connections) == 0
	}) {
		t.Errorf("连接关闭后应从节点状态移除")
	}

	// 本地转发不采集访问历史（沿用代理监听器的访问日志开关）
	m.history.Add(proxy.AccessRecord{TunnelID: cfg.ID, TunnelName: cfg.Name, Target: "example.com:80", Result: "成功"})
	status = m.NodeStatus(cfgs, 50)
	if len(status.AccessHistory) != 1 || status.AccessHistory[0].Target != "example.com:80" {
		t.Errorf("访问历史聚合不符: %+v", status.AccessHistory)
	}
	// 已删除配置的历史应被过滤
	if got := m.NodeStatus(nil, 50); len(got.AccessHistory) != 0 {
		t.Errorf("未知隧道的历史应被过滤: %+v", got.AccessHistory)
	}

	if err := m.Stop("node-status"); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	stopped := m.NodeStatus(cfgs, 50)
	if stopped.TunnelRunning != 0 || stopped.ForwardCount != 0 || stopped.ActiveConns != 0 {
		t.Errorf("停止后节点概览不符: %+v", stopped)
	}
	if stopped.TotalConns < 1 || stopped.BytesSent < 4 || stopped.BytesRecv < 4 {
		t.Errorf("停止后应保留累计流量: %+v", stopped)
	}
	if len(stopped.Tunnels) != 1 || stopped.Tunnels[0].Running {
		t.Errorf("停止后隧道汇总不符: %+v", stopped.Tunnels)
	}
}
