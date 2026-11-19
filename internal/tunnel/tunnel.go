package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
	"sshtunnel/internal/port"
	"sshtunnel/internal/proxy"
	"sshtunnel/internal/sshclient"
)

// 重连参数（var 以便测试覆盖，避免真实等待）
var (
	reconnectInitialDelay = 2 * time.Second
	reconnectMaxDelay     = 60 * time.Second
	reconnectMaxAttempts  = 0 // 0 表示无限重连
)

// tunnelClient 抽象 SSH 客户端，使隧道逻辑可在不建立真实 SSH 连接的前提下测试。
// *sshclient.Client 满足该接口。
type tunnelClient interface {
	Dial(network, address string) (net.Conn, error)
	Wait() error
	Close() error
	StartKeepAlive(interval time.Duration) func()
}

// sshDialer 抽象 SSH 连接的建立，测试可注入返回 mock 客户端的实现。
type sshDialer interface {
	Dial(hops []sshclient.Hop) (tunnelClient, error)
}

// defaultDialer 委托给真实的 sshclient.Dial。
type defaultDialer struct{}

func (defaultDialer) Dial(hops []sshclient.Hop) (tunnelClient, error) {
	return sshclient.Dial(hops)
}

// dialer 是包级 SSH 拨号器，测试可通过 setDialer 替换。
var dialer sshDialer = defaultDialer{}

// Tunnel 单条隧道的运行实例
type Tunnel struct {
	mu            sync.Mutex
	cfg           *config.TunnelConfig
	client        tunnelClient
	proxyMgr      *proxy.Manager
	fwdListeners  []net.Listener
	fwdStats      map[string]*proxy.TrafficStats // 本地转发 ID -> 统计
	conns         *proxy.ConnRegistry            // 活动连接注册表（由 Manager 注入）
	history       *proxy.AccessHistory           // 结构化访问历史（由 Manager 注入）
	ctx           context.Context
	cancel        context.CancelFunc
	log           *logger.Logger
	reconnecting  bool
	stopCh        chan struct{}
	keepAliveStop func() // 停止 SSH 心跳（nil 表示未启用）
	// onFatal 致命错误回调（如重连次数耗尽），由 Manager 注入用于移除运行实例并更新状态
	onFatal func()
}

// Manager 隧道管理器
type Manager struct {
	mu              sync.RWMutex
	tunnels         map[string]*Tunnel
	archived        map[string]TunnelStats // 已停止隧道的累计汇总（进程内保留）
	conns           *proxy.ConnRegistry    // 活动连接注册表（全节点共享）
	history         *proxy.AccessHistory   // 结构化访问历史（全节点共享）
	log             *logger.Logger
	startedAt       time.Time
	statusCallbacks []func(tunnelID string, status string)
}

// accessHistoryCapacity 访问历史最大保留条数（进程内环形缓冲）
const accessHistoryCapacity = 2000

// NewManager 创建隧道管理器
func NewManager(log *logger.Logger) *Manager {
	return &Manager{
		tunnels:   make(map[string]*Tunnel),
		archived:  make(map[string]TunnelStats),
		conns:     proxy.NewConnRegistry(),
		history:   proxy.NewAccessHistory(accessHistoryCapacity),
		log:       log,
		startedAt: time.Now(),
	}
}

// OnStatusChange 注册状态变更回调（支持多订阅者）
func (m *Manager) OnStatusChange(cb func(tunnelID string, status string)) {
	m.mu.Lock()
	m.statusCallbacks = append(m.statusCallbacks, cb)
	m.mu.Unlock()
}

// errTunnelStopped 隧道已被主动停止（用于中断进行中的建连/重连）
var errTunnelStopped = errors.New("隧道已停止")

// Start 启动指定隧道
func (m *Manager) Start(cfg *config.TunnelConfig) error {
	t := &Tunnel{
		cfg:     cfg,
		log:     m.log,
		stopCh:  make(chan struct{}),
		conns:   m.conns,
		history: m.history,
	}
	// 致命错误（如重连次数耗尽）：移除运行实例并通知状态变更，
	// 避免隧道实际已断开却仍显示「运行中」。
	t.onFatal = func() {
		// 主动停止与重连耗尽可能同时发生：已停止时不再上报致命错误
		if t.isStopped() {
			return
		}
		archive := false
		m.mu.Lock()
		if cur, ok := m.tunnels[cfg.ID]; ok && cur == t {
			delete(m.tunnels, cfg.ID)
			archive = true
		}
		m.mu.Unlock()
		if t.cancel != nil {
			t.cancel()
		}
		if archive {
			m.archive(cfg.ID, t)
		}
		m.emitStatus(cfg.ID, string(config.StatusError))
	}

	// 先占位再拨号：并发/重复 Start 不会重复建连，拨号期间 Stop 也能找到实例
	m.mu.Lock()
	if _, exists := m.tunnels[cfg.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("隧道已在运行: %s", cfg.ID)
	}
	m.tunnels[cfg.ID] = t
	m.mu.Unlock()

	t.ctx, t.cancel = context.WithCancel(context.Background())
	if err := t.run(); err != nil {
		m.mu.Lock()
		if cur, ok := m.tunnels[cfg.ID]; ok && cur == t {
			delete(m.tunnels, cfg.ID)
		}
		m.mu.Unlock()
		if t.cancel != nil {
			t.cancel()
		}
		t.cleanupResources()
		t.conns.RemoveTunnel(cfg.ID)
		return err
	}

	// run 期间可能被 Stop 或 onFatal 移除
	m.mu.RLock()
	_, registered := m.tunnels[cfg.ID]
	m.mu.RUnlock()
	if !registered {
		t.cleanupResources()
		return fmt.Errorf("隧道启动中断: %s", cfg.ID)
	}

	m.emitStatus(cfg.ID, string(config.StatusRunning))
	m.log.TunnelInfo(cfg.ID, "隧道已启动: "+cfg.Name)
	return nil
}

// Stop 停止指定隧道
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	t, ok := m.tunnels[id]
	delete(m.tunnels, id)
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("隧道未运行: %s", id)
	}
	t.stop()
	m.archive(id, t)
	m.emitStatus(id, string(config.StatusStopped))
	m.log.TunnelInfo(id, "隧道已停止")
	return nil
}

// StopAll 停止所有隧道
func (m *Manager) StopAll() {
	m.mu.Lock()
	tunnels := m.tunnels
	m.tunnels = make(map[string]*Tunnel)
	m.mu.Unlock()
	for id, t := range tunnels {
		t.stop()
		m.archive(id, t)
		m.emitStatus(id, string(config.StatusStopped))
	}
}

// IsRunning 检查隧道是否在运行
func (m *Manager) IsRunning(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.tunnels[id]
	return ok
}

// ForwardStats 本地端口转发的运行统计
type ForwardStats struct {
	ID          string `json:"id"`
	LocalPort   int    `json:"local_port"`
	RemoteHost  string `json:"remote_host"`
	RemotePort  int    `json:"remote_port"`
	ActiveConns int64  `json:"active_conns"`
	TotalConns  int64  `json:"total_conns"`
	BytesSent   int64  `json:"bytes_sent"` // 上行（客户端 -> 目标）
	BytesRecv   int64  `json:"bytes_recv"` // 下行（目标 -> 客户端）
}

// TunnelStats 单条隧道的连接与流量统计
type TunnelStats struct {
	ID          string                `json:"id"`
	Listeners   []proxy.ListenerStats `json:"listeners,omitempty"`
	Forwards    []ForwardStats        `json:"forwards,omitempty"`
	ActiveConns int64                 `json:"active_conns"`
	TotalConns  int64                 `json:"total_conns"`
	BytesSent   int64                 `json:"bytes_sent"`
	BytesRecv   int64                 `json:"bytes_recv"`
}

// Stats 返回该隧道的连接与流量统计快照
func (t *Tunnel) Stats() TunnelStats {
	t.mu.Lock()
	pm := t.proxyMgr
	fwd := make(map[string]*proxy.TrafficStats, len(t.fwdStats))
	for id, s := range t.fwdStats {
		fwd[id] = s
	}
	t.mu.Unlock()

	out := TunnelStats{ID: t.cfg.ID}
	if pm != nil {
		out.Listeners = pm.Stats()
	}
	for _, lf := range t.cfg.LocalForwards {
		fs := ForwardStats{
			ID:         lf.ID,
			LocalPort:  lf.LocalPort,
			RemoteHost: lf.RemoteHost,
			RemotePort: lf.RemotePort,
		}
		if c, ok := fwd[lf.ID]; ok {
			fs.ActiveConns, fs.TotalConns, fs.BytesSent, fs.BytesRecv = c.Snapshot()
		}
		out.Forwards = append(out.Forwards, fs)
	}

	// 聚合到隧道级别
	for _, l := range out.Listeners {
		out.ActiveConns += l.ActiveConns
		out.TotalConns += l.TotalConns
		out.BytesSent += l.BytesSent
		out.BytesRecv += l.BytesRecv
	}
	for _, f := range out.Forwards {
		out.ActiveConns += f.ActiveConns
		out.TotalConns += f.TotalConns
		out.BytesSent += f.BytesSent
		out.BytesRecv += f.BytesRecv
	}
	return out
}

// Stats 返回所有隧道（含已停止）的统计（以隧道 ID 为键）。
// 已停止隧道的累计值保留在归档中，重启后与新的运行统计相加，形成进程内累计。
func (m *Manager) Stats() map[string]TunnelStats {
	m.mu.RLock()
	tunnels := make([]*Tunnel, 0, len(m.tunnels))
	for _, t := range m.tunnels {
		tunnels = append(tunnels, t)
	}
	archived := make(map[string]TunnelStats, len(m.archived))
	for id, st := range m.archived {
		archived[id] = st
	}
	m.mu.RUnlock()

	out := make(map[string]TunnelStats, len(tunnels)+len(archived))
	for id, st := range archived {
		out[id] = st
	}
	for _, t := range tunnels {
		st := t.Stats()
		if prev, ok := archived[t.cfg.ID]; ok {
			st.TotalConns += prev.TotalConns
			st.BytesSent += prev.BytesSent
			st.BytesRecv += prev.BytesRecv
		}
		out[t.cfg.ID] = st
	}
	return out
}

// archive 归档已停止隧道的累计汇总（不保留监听器/转发明细，避免展示不存在的监听端口）
func (m *Manager) archive(id string, t *Tunnel) {
	snap := t.Stats()
	snap.ID = id
	snap.Listeners = nil
	snap.Forwards = nil
	snap.ActiveConns = 0
	m.mu.Lock()
	prev := m.archived[id]
	prev.ID = id
	prev.TotalConns += snap.TotalConns
	prev.BytesSent += snap.BytesSent
	prev.BytesRecv += snap.BytesRecv
	m.archived[id] = prev
	m.mu.Unlock()
}

// Forget 清理隧道的历史累计与残留连接（删除配置时调用）
func (m *Manager) Forget(id string) {
	m.mu.Lock()
	delete(m.archived, id)
	m.mu.Unlock()
	if m.conns != nil {
		m.conns.RemoveTunnel(id)
	}
}

// TunnelTraffic 单条隧道的流量汇总（供节点状态页展示）
type TunnelTraffic struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Running     bool   `json:"running"`
	ActiveConns int64  `json:"active_conns"`
	TotalConns  int64  `json:"total_conns"`
	BytesSent   int64  `json:"bytes_sent"`
	BytesRecv   int64  `json:"bytes_recv"`
}

// NodeStatus 节点级运行状态（连接、流量与访问历史）
type NodeStatus struct {
	Version       string               `json:"version"`
	StartedAt     time.Time            `json:"started_at"`
	UptimeSeconds int64                `json:"uptime_seconds"`
	TunnelTotal   int                  `json:"tunnel_total"`
	TunnelRunning int                  `json:"tunnel_running"`
	ListenerCount int                  `json:"listener_count"`
	ForwardCount  int                  `json:"forward_count"`
	ActiveConns   int64                `json:"active_conns"`
	TotalConns    int64                `json:"total_conns"`
	BytesSent     int64                `json:"bytes_sent"`
	BytesRecv     int64                `json:"bytes_recv"`
	Tunnels       []TunnelTraffic      `json:"tunnels"`
	Connections   []proxy.ConnSnapshot `json:"connections"`
	AccessHistory []proxy.AccessRecord `json:"access_history"`
}

// NodeHistory 返回指定隧道最近的访问历史（时间倒序）
func (m *Manager) NodeHistory(tunnelID string, limit int) []proxy.AccessRecord {
	if tunnelID == "" {
		return []proxy.AccessRecord{}
	}
	if limit <= 0 {
		limit = 500
	}
	if limit > 1000 {
		limit = 1000
	}
	return m.history.RecentByTunnel(tunnelID, limit)
}

// NodeStatus 汇总本节点运行状态。
// cfgs 为全部隧道配置，用于区分运行/停止状态并补齐名称；historyLimit 为访问历史条数上限。
func (m *Manager) NodeStatus(cfgs []config.TunnelConfig, historyLimit int) NodeStatus {
	stats := m.Stats()
	out := NodeStatus{
		StartedAt:     m.startedAt,
		UptimeSeconds: int64(time.Since(m.startedAt).Seconds()),
		TunnelTotal:   len(cfgs),
		Connections:   make([]proxy.ConnSnapshot, 0),
		Tunnels:       make([]TunnelTraffic, 0, len(cfgs)),
	}
	known := make(map[string]bool, len(cfgs))
	for _, cfg := range cfgs {
		known[cfg.ID] = true
		st := stats[cfg.ID]
		running := m.IsRunning(cfg.ID)
		if running {
			out.TunnelRunning++
			out.ListenerCount += len(st.Listeners)
			out.ForwardCount += len(st.Forwards)
		}
		out.ActiveConns += st.ActiveConns
		out.TotalConns += st.TotalConns
		out.BytesSent += st.BytesSent
		out.BytesRecv += st.BytesRecv
		out.Tunnels = append(out.Tunnels, TunnelTraffic{
			ID:          cfg.ID,
			Name:        cfg.Name,
			Running:     running,
			ActiveConns: st.ActiveConns,
			TotalConns:  st.TotalConns,
			BytesSent:   st.BytesSent,
			BytesRecv:   st.BytesRecv,
		})
	}
	// 仅展示仍存在的配置对应的活动连接（清理已删除配置的残留）
	for _, c := range m.conns.Snapshot() {
		if known[c.TunnelID] {
			out.Connections = append(out.Connections, c)
		}
	}
	// 访问历史可能包含已删除的隧道，按当前配置过滤
	for _, rec := range m.history.Recent(historyLimit) {
		if known[rec.TunnelID] {
			out.AccessHistory = append(out.AccessHistory, rec)
		}
	}
	if out.AccessHistory == nil {
		out.AccessHistory = []proxy.AccessRecord{}
	}
	return out
}

// emitStatus 触发状态变更回调（通知所有订阅者）
func (m *Manager) emitStatus(tunnelID, status string) {
	m.mu.RLock()
	callbacks := make([]func(string, string), len(m.statusCallbacks))
	copy(callbacks, m.statusCallbacks)
	m.mu.RUnlock()
	for _, cb := range callbacks {
		cb(tunnelID, status)
	}
}

// run 启动 SSH 连接、本地转发、代理监听
func (t *Tunnel) run() error {
	if err := t.connectAndServe(); err != nil {
		return err
	}

	// 启动断线监听 + 自动重连
	if t.cfg.AutoReconnect {
		go t.watchAndReconnect()
	} else {
		go t.watchOnly()
	}
	return nil
}

// connectAndServe 建立 SSH 连接并启动所有监听服务
func (t *Tunnel) connectAndServe() error {
	if t.isStopped() {
		return errTunnelStopped
	}
	hops := make([]sshclient.Hop, len(t.cfg.HopChain))
	for i, h := range t.cfg.HopChain {
		hops[i] = sshclient.Hop{
			User:       h.User,
			Host:       h.Host,
			Port:       h.Port,
			AuthType:   h.AuthType,
			Password:   h.Password,
			KeyContent: h.KeyContent,
			Passphrase: h.Passphrase,
		}
	}
	client, err := dialer.Dial(hops)
	if err != nil {
		t.log.TunnelError(t.cfg.ID, "SSH 连接失败: "+err.Error())
		return err
	}
	// 启动 SSH 心跳（可配置，0 表示关闭）
	var keepAliveStop func()
	if t.cfg.KeepAliveSeconds > 0 {
		keepAliveStop = client.StartKeepAlive(time.Duration(t.cfg.KeepAliveSeconds) * time.Second)
	}
	t.mu.Lock()
	t.client = client
	t.keepAliveStop = keepAliveStop
	t.mu.Unlock()

	// 启动本地端口转发
	for _, lf := range t.cfg.LocalForwards {
		if err := t.startLocalForward(lf); err != nil {
			t.log.TunnelError(t.cfg.ID, fmt.Sprintf("本地转发启动失败 :%d: %s", lf.LocalPort, err.Error()))
			return fmt.Errorf("本地转发 :%d 启动失败: %w", lf.LocalPort, err)
		}
	}

	// 启动代理监听（复用同一个 Manager，使统计对象跨重连保留）
	if len(t.cfg.ProxyListeners) > 0 {
		t.mu.Lock()
		pm := t.proxyMgr
		if pm == nil {
			pm = proxy.NewManager(client, t.log, t.cfg.ID, t.cfg.Name)
			pm.SetTracker(t.conns, t.history)
			t.proxyMgr = pm
		}
		t.mu.Unlock()
		pm.SetDialer(client) // 重连后指向新的 SSH 连接
		if err := pm.StartAll(t.ctx, t.cfg.ProxyListeners); err != nil {
			t.log.TunnelError(t.cfg.ID, "代理监听启动失败: "+err.Error())
			return fmt.Errorf("代理监听启动失败: %w", err)
		}
	}
	return nil
}

// clientSafe 在锁保护下获取当前客户端快照（阻塞操作须在锁外调用）。
func (t *Tunnel) clientSafe() tunnelClient {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.client
}

// watchOnly 仅监听断开（不重连）
func (t *Tunnel) watchOnly() {
	c := t.clientSafe()
	if c == nil {
		return
	}
	err := c.Wait()
	if t.isStopped() {
		return
	}
	if err != nil {
		t.log.TunnelWarn(t.cfg.ID, "SSH 连接断开: "+err.Error())
	} else {
		t.log.TunnelWarn(t.cfg.ID, "SSH 连接已关闭")
	}
	t.log.TunnelError(t.cfg.ID, "未启用自动重连，隧道已停止")
	// 连接已不可用：移除运行实例并上报错误，避免界面长期显示「运行中」
	if t.onFatal != nil {
		t.onFatal()
	}
}

// watchAndReconnect 监听断开并自动重连
func (t *Tunnel) watchAndReconnect() {
	for {
		select {
		case <-t.stopCh:
			return
		default:
		}

		c := t.clientSafe()
		if c == nil {
			return
		}
		err := c.Wait()
		if t.isStopped() {
			return
		}
		if err != nil {
			t.log.TunnelWarn(t.cfg.ID, "SSH 连接断开: "+err.Error())
		}

		t.log.TunnelInfo(t.cfg.ID, "开始自动重连...")
		t.reconnecting = true
		t.cleanupResources()

		// 重连策略：最大退避与最大次数均可按隧道配置（0 表示使用默认/无限）
		maxDelay := reconnectMaxDelay
		if v := t.cfg.ReconnectMaxDelaySeconds; v > 0 {
			maxDelay = time.Duration(v) * time.Second
		}
		maxAttempts := reconnectMaxAttempts
		if v := t.cfg.MaxReconnectAttempts; v > 0 {
			maxAttempts = v
		}

		delay := reconnectInitialDelay
		attempts := 0
		for {
			select {
			case <-t.stopCh:
				t.reconnecting = false
				return
			case <-time.After(delay):
			}

			attempts++
			t.log.TunnelInfo(t.cfg.ID, fmt.Sprintf("第 %d 次重连尝试...", attempts))
			if err := t.connectAndServe(); err != nil {
				// 停止过程中产生的错误不再计为失败
				if t.isStopped() {
					t.reconnecting = false
					return
				}
				// 清理本次可能已部分启动的监听器，避免下次重连端口冲突
				t.cleanupResources()
				t.log.TunnelWarn(t.cfg.ID, fmt.Sprintf("第 %d 次重连失败: %s", attempts, err.Error()))
				if maxAttempts > 0 && attempts >= maxAttempts {
					t.reconnecting = false
					if t.isStopped() {
						return
					}
					t.log.TunnelError(t.cfg.ID, fmt.Sprintf("已达到最大重连次数 %d，停止自动重连", maxAttempts))
					if t.onFatal != nil {
						t.onFatal()
					}
					return
				}
				// 指数退避
				delay = delay * 2
				if delay > maxDelay {
					delay = maxDelay
				}
				continue
			}
			// 停止与重连建连并发：已停止时清理本次新建的资源
			if t.isStopped() {
				t.reconnecting = false
				t.cleanupResources()
				return
			}
			t.reconnecting = false
			t.log.TunnelInfo(t.cfg.ID, fmt.Sprintf("第 %d 次重连成功", attempts))
			break
		}
	}
}

// cleanupResources 清理旧连接资源（不取消 context）。
// 先在锁内快照并清空字段，再在锁外执行阻塞的 Close/StopAll，避免数据竞争。
func (t *Tunnel) cleanupResources() {
	t.mu.Lock()
	listeners := t.fwdListeners
	t.fwdListeners = nil
	// 保留 proxyMgr 实例（只停止监听），以便重连后复用其统计计数
	pm := t.proxyMgr
	c := t.client
	t.client = nil
	stopKeepAlive := t.keepAliveStop
	t.keepAliveStop = nil
	t.mu.Unlock()

	if stopKeepAlive != nil {
		stopKeepAlive()
	}
	for _, ln := range listeners {
		ln.Close()
	}
	if pm != nil {
		pm.StopAll()
	}
	if c != nil {
		c.Close()
	}
}

// isStopped 判断隧道是否已被主动停止
func (t *Tunnel) isStopped() bool {
	select {
	case <-t.stopCh:
		return true
	default:
		return false
	}
}

// startLocalForward 启动单个本地端口转发
func (t *Tunnel) startLocalForward(lf config.LocalForward) error {
	addr := port.BindAddress(lf.LocalPort, lf.AllowExternal)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.fwdListeners = append(t.fwdListeners, ln)
	if t.fwdStats == nil {
		t.fwdStats = make(map[string]*proxy.TrafficStats)
	}
	// 复用同一转发 ID 的统计对象：重连后「累计」数据不清零
	stats := t.fwdStats[lf.ID]
	if stats == nil {
		stats = &proxy.TrafficStats{}
		t.fwdStats[lf.ID] = stats
	}
	t.mu.Unlock()

	target := fmt.Sprintf("%s:%d", lf.RemoteHost, lf.RemotePort)
	t.log.TunnelInfo(t.cfg.ID, "本地转发已启动: "+addr+" -> "+target)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				if t.isStopped() {
					return
				}
				t.log.TunnelError(t.cfg.ID, "接受本地转发连接失败: "+err.Error())
				return
			}
			go t.handleForward(conn, lf, target, stats)
		}
	}()
	return nil
}

func (t *Tunnel) handleForward(conn net.Conn, lf config.LocalForward, target string, stats *proxy.TrafficStats) {
	defer conn.Close()
	stats.BeginConn()
	defer stats.EndConn()
	c := t.clientSafe()
	if c == nil {
		conn.Close()
		return
	}
	targetConn, err := c.Dial("tcp", target)
	if err != nil {
		t.log.TunnelWarn(t.cfg.ID, "转发连接目标失败 "+target+": "+err.Error())
		return
	}
	defer targetConn.Close()

	// 目标已连通：登记为活动连接（桥接结束时注销）
	var entry *proxy.ConnEntry
	if t.conns != nil {
		entry = t.conns.Open(conn, proxy.ConnMeta{
			TunnelID:   t.cfg.ID,
			TunnelName: t.cfg.Name,
			ListenerID: lf.ID,
			Protocol:   "local",
			ListenPort: lf.LocalPort,
			Target:     target,
		})
		defer t.conns.Close(entry)
	}
	proxy.BridgeStats(conn, targetConn, stats, entry)
}

// stop 停止隧道
func (t *Tunnel) stop() {
	if t.cancel != nil {
		t.cancel()
	}
	close(t.stopCh)
	t.cleanupResources()
	// 隧道停止后清理其活动连接，避免状态页展示已断开的残留连接
	if t.conns != nil {
		t.conns.RemoveTunnel(t.cfg.ID)
	}
}
