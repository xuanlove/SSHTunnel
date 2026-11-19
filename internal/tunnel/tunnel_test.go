package tunnel

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
	"sshtunnel/internal/sshclient"
)

// ===== Mock SSH 客户端与拨号器 =====
// 通过抽象的 tunnelClient / sshDialer 接口，可在不建立真实 SSH 连接的前提下
// 测试隧道生命周期、自动重连与状态回调。

// mockClient 模拟 SSH 客户端：Wait 阻塞直到 Close 被调用（模拟连接断开）。
type mockClient struct {
	closeOnce     sync.Once
	closed        chan struct{}
	dialFn        func(network, address string) (net.Conn, error)
	keepAliveFail time.Duration // >0 时模拟心跳失败：到点自动关闭连接
}

func newMockClient() *mockClient {
	return &mockClient{closed: make(chan struct{})}
}

func (m *mockClient) Dial(network, address string) (net.Conn, error) {
	if m.dialFn != nil {
		return m.dialFn(network, address)
	}
	return nil, errors.New("mock: dial not configured")
}

func (m *mockClient) Wait() error {
	<-m.closed
	return errors.New("mock connection closed")
}

func (m *mockClient) Close() error {
	m.closeOnce.Do(func() { close(m.closed) })
	return nil
}

func (m *mockClient) StartKeepAlive(interval time.Duration) func() {
	if m.keepAliveFail > 0 {
		go func() {
			time.Sleep(m.keepAliveFail)
			_ = m.Close() // 模拟心跳失败后连接断开
		}()
	}
	return func() {}
}

// dialResult 单次拨号结果：err 非空则失败，否则返回新 mockClient。
type dialResult struct {
	err error
}

// mockDialer 按预设序列返回结果，记录调用次数与最后一次返回的客户端。
type mockDialer struct {
	mu            sync.Mutex
	results       []dialResult
	calls         int
	last          *mockClient
	dialFn        func(network, address string) (net.Conn, error)
	keepAliveFail time.Duration
}

func (d *mockDialer) Dial(hops []sshclient.Hop) (tunnelClient, error) {
	d.mu.Lock()
	d.calls++
	idx := d.calls - 1
	var r dialResult
	if idx < len(d.results) {
		r = d.results[idx]
	}
	d.mu.Unlock()

	if r.err != nil {
		return nil, r.err
	}
	c := newMockClient()
	d.mu.Lock()
	c.dialFn = d.dialFn
	c.keepAliveFail = d.keepAliveFail
	d.last = c
	d.mu.Unlock()
	return c, nil
}

func (d *mockDialer) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *mockDialer) lastClient() *mockClient {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.last
}

// ===== 测试辅助 =====

// withMockDialer 临时替换包级拨号器与重连延迟，测试结束自动恢复。
func withMockDialer(t *testing.T, d *mockDialer) {
	t.Helper()
	oldDialer := dialer
	oldInit := reconnectInitialDelay
	oldMax := reconnectMaxDelay
	dialer = d
	reconnectInitialDelay = 5 * time.Millisecond
	reconnectMaxDelay = 20 * time.Millisecond
	t.Cleanup(func() {
		dialer = oldDialer
		reconnectInitialDelay = oldInit
		reconnectMaxDelay = oldMax
	})
}

// newTestManager 创建带 mock 日志的管理器。
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(logger.NewTest())
}

// testConfig 构造无监听器的最小隧道配置（仅测试 SSH 连接生命周期）。
func testConfig(id string, autoReconnect bool) *config.TunnelConfig {
	return &config.TunnelConfig{
		ID:            id,
		Name:          "测试隧道-" + id,
		HopChain:      []config.HopConfig{{User: "u", Host: "h", Port: 22, AuthType: "password", Password: "p"}},
		TunnelType:    config.TunnelProxy,
		AutoReconnect: autoReconnect,
	}
}

// statusCollector 收集状态回调，提供按值断言。
type statusCollector struct {
	mu     sync.Mutex
	events []string
}

func (s *statusCollector) cb(tunnelID, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, status)
}

func (s *statusCollector) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.events))
	copy(out, s.events)
	return out
}

// waitFor 轮询直到条件成立或超时。
func waitFor(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fn()
}

// ===== 测试用例 =====

func TestManagerStartStop(t *testing.T) {
	d := &mockDialer{results: []dialResult{{}}} // 单次成功
	withMockDialer(t, d)
	m := newTestManager(t)

	sc := &statusCollector{}
	m.OnStatusChange(sc.cb)

	if err := m.Start(testConfig("t1", false)); err != nil {
		t.Fatalf("启动失败: %v", err)
	}

	if !m.IsRunning("t1") {
		t.Errorf("启动后 IsRunning 应为 true")
	}
	if sc.snapshot()[0] != string(config.StatusRunning) {
		t.Errorf("应收到 running 状态，got %v", sc.snapshot())
	}

	if err := m.Stop("t1"); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
	if m.IsRunning("t1") {
		t.Errorf("停止后 IsRunning 应为 false")
	}

	// 关闭后最近一条状态应为 stopped
	if got := sc.snapshot(); got[len(got)-1] != string(config.StatusStopped) {
		t.Errorf("应收到 stopped 状态，got %v", got)
	}
}

func TestStartAlreadyRunning(t *testing.T) {
	d := &mockDialer{results: []dialResult{{}}}
	withMockDialer(t, d)
	m := newTestManager(t)

	cfg := testConfig("dup", false)
	if err := m.Start(cfg); err != nil {
		t.Fatalf("首次启动失败: %v", err)
	}
	if err := m.Start(cfg); err == nil {
		t.Errorf("重复启动应报错")
	}
	_ = m.Stop("dup")
}

func TestStopNotRunning(t *testing.T) {
	m := newTestManager(t)
	if err := m.Stop("nonexistent"); err == nil {
		t.Errorf("停止未运行的隧道应报错")
	}
}

func TestStopAll(t *testing.T) {
	d := &mockDialer{results: []dialResult{{}, {}}} // 两次成功
	withMockDialer(t, d)
	m := newTestManager(t)

	for _, id := range []string{"a", "b"} {
		if err := m.Start(testConfig(id, false)); err != nil {
			t.Fatalf("启动 %s 失败: %v", id, err)
		}
	}

	m.StopAll()
	if m.IsRunning("a") || m.IsRunning("b") {
		t.Errorf("StopAll 后所有隧道应停止")
	}
}

func TestOnStatusChangeMultipleSubscribers(t *testing.T) {
	d := &mockDialer{results: []dialResult{{}}}
	withMockDialer(t, d)
	m := newTestManager(t)

	var mu sync.Mutex
	var got1, got2 []string
	m.OnStatusChange(func(id, status string) {
		mu.Lock()
		got1 = append(got1, status)
		mu.Unlock()
	})
	m.OnStatusChange(func(id, status string) {
		mu.Lock()
		got2 = append(got2, status)
		mu.Unlock()
	})

	_ = m.Start(testConfig("multi", false))
	_ = m.Stop("multi")

	if !waitFor(time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got1) >= 2 && len(got2) >= 2
	}) {
		t.Errorf("两个订阅者都应收到至少 2 次状态变更, got1=%v got2=%v", got1, got2)
	}
}

// TestReconnect 验证自动重连：连接断开后按指数退避重试，失败再成功。
func TestReconnect(t *testing.T) {
	d := &mockDialer{results: []dialResult{
		{},                             // 首次启动成功
		{err: errors.New("dial fail")}, // 重连第 1 次失败
		{},                             // 重连第 2 次成功
	}}
	withMockDialer(t, d)
	m := newTestManager(t)

	if err := m.Start(testConfig("rc", true)); err != nil {
		t.Fatalf("启动失败: %v", err)
	}

	// 触发连接断开：关闭首个客户端，使 watchAndReconnect 的 Wait 返回
	first := d.lastClient()
	if first == nil {
		t.Fatal("应为首次连接返回 mock 客户端")
	}
	first.Close()

	// 期望：重连尝试 3 次（1 成功 + 1 失败 + 1 成功）
	if !waitFor(2*time.Second, func() bool {
		return d.callCount() == 3
	}) {
		t.Errorf("应完成 3 次拨号尝试，实际 %d", d.callCount())
	}

	// 重连成功后隧道仍应处于运行态
	if !m.IsRunning("rc") {
		t.Errorf("重连成功后隧道应仍在运行")
	}

	if err := m.Stop("rc"); err != nil {
		t.Fatalf("停止失败: %v", err)
	}
}

// TestReconnectMaxAttemptsMarksError 验证重连次数耗尽后隧道被移除运行列表并上报 error 状态。
func TestReconnectMaxAttemptsMarksError(t *testing.T) {
	d := &mockDialer{results: []dialResult{
		{}, // 首次启动成功
		{err: errors.New("dial fail")},
		{err: errors.New("dial fail")},
	}}
	withMockDialer(t, d)
	m := newTestManager(t)
	sc := &statusCollector{}
	m.OnStatusChange(sc.cb)

	cfg := testConfig("max-attempts", true)
	cfg.MaxReconnectAttempts = 2
	if err := m.Start(cfg); err != nil {
		t.Fatalf("启动失败: %v", err)
	}

	d.lastClient().Close()

	// 两次重连失败后应停止并移除运行实例
	if !waitFor(2*time.Second, func() bool { return !m.IsRunning("max-attempts") }) {
		t.Fatalf("重连耗尽后应从运行列表移除，拨号次数=%d", d.callCount())
	}

	statuses := sc.snapshot()
	found := false
	for _, s := range statuses {
		if s == string(config.StatusError) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("应上报 error 状态，实际状态序列: %v", statuses)
	}
	if _, ok := m.Stats()["max-attempts"]; ok {
		t.Errorf("重连耗尽后不应再出现在运行统计中")
	}
}

// TestLocalForwardStatsAcrossReconnect 本地转发统计应记录连接/流量，且重连后累计不清零。
func TestLocalForwardStatsAcrossReconnect(t *testing.T) {
	// 回显服务器
	echoLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动回显服务器失败: %v", err)
	}
	defer echoLn.Close()
	go func() {
		for {
			c, err := echoLn.Accept()
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

	// 选一个空闲本地端口
	fl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("分配本地端口失败: %v", err)
	}
	localPort := fl.Addr().(*net.TCPAddr).Port
	_ = fl.Close()

	d := &mockDialer{results: []dialResult{{}, {}}} // 初次连接 + 重连
	d.dialFn = func(network, address string) (net.Conn, error) {
		return net.Dial("tcp", echoLn.Addr().String())
	}
	withMockDialer(t, d)

	m := newTestManager(t)
	cfg := &config.TunnelConfig{
		ID:            "lf-stats",
		Name:          "本地转发统计",
		HopChain:      []config.HopConfig{{User: "u", Host: "h", Port: 22, AuthType: "password", Password: "p"}},
		TunnelType:    config.TunnelLocalForward,
		AutoReconnect: true,
		LocalForwards: []config.LocalForward{
			{ID: "lf-1", LocalPort: localPort, RemoteHost: "127.0.0.1", RemotePort: 80},
		},
	}
	if err := m.Start(cfg); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer m.Stop("lf-stats")

	addr := fmt.Sprintf("127.0.0.1:%d", localPort)
	doSession := func() error {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return err
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := conn.Write([]byte("ping")); err != nil {
			return err
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return err
		}
		if string(buf) != "ping" {
			return fmt.Errorf("回显不符: %q", buf)
		}
		return nil
	}

	if err := doSession(); err != nil {
		t.Fatalf("首次转发失败: %v", err)
	}

	// 首次会话统计：1 次连接、上下行各 4 字节
	deadline := time.Now().Add(2 * time.Second)
	var st TunnelStats
	for {
		st = m.Stats()["lf-stats"]
		if (len(st.Forwards) == 1 && st.Forwards[0].TotalConns == 1 &&
			st.Forwards[0].BytesSent == 4 && st.Forwards[0].BytesRecv == 4) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(st.Forwards) != 1 {
		t.Fatalf("应返回 1 条转发统计，实际 %d", len(st.Forwards))
	}
	fw := st.Forwards[0]
	if fw.ID != "lf-1" || fw.LocalPort != localPort {
		t.Errorf("转发标识不符: %+v", fw)
	}
	if fw.TotalConns != 1 || fw.BytesSent != 4 || fw.BytesRecv != 4 {
		t.Errorf("首次会话统计不符: %+v", fw)
	}

	// 模拟 SSH 断线并重连
	d.lastClient().Close()
	if !waitFor(2*time.Second, func() bool { return d.callCount() >= 2 }) {
		t.Fatalf("应触发重连，拨号次数=%d", d.callCount())
	}
	if !waitFor(2*time.Second, func() bool { return doSession() == nil }) {
		t.Fatalf("重连后本地转发不可用")
	}

	// 累计统计应保留：2 次连接、上下行各 8 字节
	deadline = time.Now().Add(2 * time.Second)
	for {
		st = m.Stats()["lf-stats"]
		if (len(st.Forwards) == 1 && st.Forwards[0].TotalConns == 2 &&
			st.Forwards[0].BytesSent == 8 && st.Forwards[0].BytesRecv == 8) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(st.Forwards) != 1 {
		t.Fatalf("重连后应返回 1 条转发统计，实际 %d", len(st.Forwards))
	}
	fw = st.Forwards[0]
	if fw.TotalConns != 2 || fw.BytesSent != 8 || fw.BytesRecv != 8 {
		t.Errorf("重连后累计统计应保留并增长: %+v", fw)
	}
}

// TestHeartbeatFailureTriggersReconnect 心跳失败关闭连接后应触发自动重连。
func TestHeartbeatFailureTriggersReconnect(t *testing.T) {
	d := &mockDialer{
		results:       []dialResult{{}, {}}, // 首次 + 重连成功
		keepAliveFail: 20 * time.Millisecond,
	}
	withMockDialer(t, d)
	m := newTestManager(t)

	cfg := testConfig("hb", true)
	cfg.KeepAliveSeconds = 1
	if err := m.Start(cfg); err != nil {
		t.Fatalf("启动失败: %v", err)
	}
	defer m.Stop("hb")

	if !waitFor(2*time.Second, func() bool { return d.callCount() >= 2 }) {
		t.Fatalf("心跳失败后应触发重连，拨号次数=%d", d.callCount())
	}
	if !m.IsRunning("hb") {
		t.Errorf("重连后隧道应仍在运行")
	}
}

// TestStartDialError 验证首次连接失败时 Start 返回错误且隧道未注册。
func TestStartDialError(t *testing.T) {
	d := &mockDialer{results: []dialResult{{err: errors.New("ssh unreachable")}}}
	withMockDialer(t, d)
	m := newTestManager(t)

	if err := m.Start(testConfig("err", false)); err == nil {
		t.Errorf("首次拨号失败应使 Start 返回错误")
	}
	if m.IsRunning("err") {
		t.Errorf("拨号失败时隧道不应注册为运行中")
	}
}
