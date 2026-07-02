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
	mu           sync.Mutex
	cfg          *config.TunnelConfig
	client       tunnelClient
	proxyMgr     *proxy.Manager
	fwdListeners []net.Listener
	ctx          context.Context
	cancel       context.CancelFunc
	log          *logger.Logger
	reconnecting bool
	stopCh       chan struct{}
}

// Manager 隧道管理器
type Manager struct {
	mu              sync.RWMutex
	tunnels         map[string]*Tunnel
	log             *logger.Logger
	statusCallbacks []func(tunnelID string, status string)
}

// NewManager 创建隧道管理器
func NewManager(log *logger.Logger) *Manager {
	return &Manager{
		tunnels: make(map[string]*Tunnel),
		log:     log,
	}
}

// OnStatusChange 注册状态变更回调（支持多订阅者）
func (m *Manager) OnStatusChange(cb func(tunnelID string, status string)) {
	m.mu.Lock()
	m.statusCallbacks = append(m.statusCallbacks, cb)
	m.mu.Unlock()
}

// Start 启动指定隧道
func (m *Manager) Start(cfg *config.TunnelConfig) error {
	m.mu.Lock()
	if _, exists := m.tunnels[cfg.ID]; exists {
		m.mu.Unlock()
		return fmt.Errorf("隧道已在运行: %s", cfg.ID)
	}
	m.mu.Unlock()

	t := &Tunnel{
		cfg:    cfg,
		log:    m.log,
		stopCh: make(chan struct{}),
	}
	t.ctx, t.cancel = context.WithCancel(context.Background())

	if err := t.run(); err != nil {
		return err
	}

	m.mu.Lock()
	m.tunnels[cfg.ID] = t
	m.mu.Unlock()
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
	t.mu.Lock()
	t.client = client
	t.mu.Unlock()

	// 启动本地端口转发
	for _, lf := range t.cfg.LocalForwards {
		if err := t.startLocalForward(lf); err != nil {
			t.log.TunnelError(t.cfg.ID, fmt.Sprintf("本地转发启动失败 :%d: %s", lf.LocalPort, err.Error()))
		}
	}

	// 启动代理监听
	if len(t.cfg.ProxyListeners) > 0 {
		pm := proxy.NewManager(client, t.log, t.cfg.ID)
		t.mu.Lock()
		t.proxyMgr = pm
		t.mu.Unlock()
		if err := pm.StartAll(t.ctx, t.cfg.ProxyListeners); err != nil {
			t.log.TunnelError(t.cfg.ID, "代理监听启动失败: "+err.Error())
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
	if err != nil && !t.isStopped() {
		t.log.TunnelWarn(t.cfg.ID, "SSH 连接断开: "+err.Error())
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
				t.log.TunnelWarn(t.cfg.ID, fmt.Sprintf("第 %d 次重连失败: %s", attempts, err.Error()))
				// 指数退避
				delay = delay * 2
				if delay > reconnectMaxDelay {
					delay = reconnectMaxDelay
				}
				continue
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
	pm := t.proxyMgr
	t.proxyMgr = nil
	c := t.client
	t.client = nil
	t.mu.Unlock()

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
			go t.handleForward(conn, target)
		}
	}()
	return nil
}

func (t *Tunnel) handleForward(conn net.Conn, target string) {
	defer conn.Close()
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
	proxy.Bridge(conn, targetConn)
}

// stop 停止隧道
func (t *Tunnel) stop() {
	if t.cancel != nil {
		t.cancel()
	}
	close(t.stopCh)
	t.cleanupResources()
}

// 确保编译期接口实现
var _ = errors.New
