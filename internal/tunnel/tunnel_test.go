package tunnel

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"sshsuidao/internal/config"
	"sshsuidao/internal/logger"
	"sshsuidao/internal/sshclient"
)

// ===== Mock SSH 客户端与拨号器 =====
// 通过抽象的 tunnelClient / sshDialer 接口，可在不建立真实 SSH 连接的前提下
// 测试隧道生命周期、自动重连与状态回调。

// mockClient 模拟 SSH 客户端：Wait 阻塞直到 Close 被调用（模拟连接断开）。
type mockClient struct {
	closeOnce sync.Once
	closed    chan struct{}
}

func newMockClient() *mockClient {
	return &mockClient{closed: make(chan struct{})}
}

func (m *mockClient) Dial(network, address string) (net.Conn, error) {
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

// dialResult 单次拨号结果：err 非空则失败，否则返回新 mockClient。
type dialResult struct {
	err error
}

// mockDialer 按预设序列返回结果，记录调用次数与最后一次返回的客户端。
type mockDialer struct {
	mu      sync.Mutex
	results []dialResult
	calls   int
	last    *mockClient
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
		{},                              // 首次启动成功
		{err: errors.New("dial fail")},  // 重连第 1 次失败
		{},                              // 重连第 2 次成功
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
