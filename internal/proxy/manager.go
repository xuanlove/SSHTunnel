package proxy

import (
	"context"
	"fmt"
	"sync"

	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
)

// Manager 多端口多协议代理管理器，绑定到单条 SSH 隧道
type Manager struct {
	mu      sync.RWMutex
	servers map[string]Server // listenerID -> server
	dialer  Dialer
	log     *logger.Logger
	tunnelID string
}

// NewManager 创建代理管理器
func NewManager(dialer Dialer, log *logger.Logger, tunnelID string) *Manager {
	return &Manager{
		servers:  make(map[string]Server),
		dialer:   dialer,
		log:      log,
		tunnelID: tunnelID,
	}
}

// StartAll 启动所有配置的监听器
func (m *Manager) StartAll(ctx context.Context, listeners []config.ProxyListener) error {
	if err := checkPortConflict(listeners); err != nil {
		return err
	}
	for _, lc := range listeners {
		if err := m.StartOne(ctx, lc); err != nil {
			return fmt.Errorf("启动监听器 %s (%s:%d) 失败: %w", lc.ID, lc.Protocol, lc.ListenPort, err)
		}
	}
	return nil
}

// StartOne 启动单个监听器
func (m *Manager) StartOne(ctx context.Context, lc config.ProxyListener) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.servers[lc.ID]; exists {
		return fmt.Errorf("监听器已存在: %s", lc.ID)
	}
	srv, err := BuildServer(lc, m.dialer, func(level, msg string) {
		switch level {
		case "error":
			m.log.TunnelError(m.tunnelID, "["+string(lc.Protocol)+":"+fmt.Sprintf("%d", lc.ListenPort)+"] "+msg)
		case "warn":
			m.log.TunnelWarn(m.tunnelID, "["+string(lc.Protocol)+":"+fmt.Sprintf("%d", lc.ListenPort)+"] "+msg)
		default:
			m.log.TunnelInfo(m.tunnelID, "["+string(lc.Protocol)+":"+fmt.Sprintf("%d", lc.ListenPort)+"] "+msg)
		}
	})
	if err != nil {
		return err
	}
	m.servers[lc.ID] = srv
	go func() {
		if err := srv.Start(ctx); err != nil {
			m.log.TunnelError(m.tunnelID, "监听器 "+lc.ID+" 退出: "+err.Error())
		}
	}()
	m.log.TunnelInfo(m.tunnelID, "启动代理监听: "+string(lc.Protocol)+" :"+fmt.Sprintf("%d", lc.ListenPort))
	return nil
}

// StopOne 停止单个监听器
func (m *Manager) StopOne(id string) error {
	m.mu.Lock()
	srv, ok := m.servers[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("监听器不存在: %s", id)
	}
	delete(m.servers, id)
	m.mu.Unlock()
	m.log.TunnelInfo(m.tunnelID, "停止代理监听: "+id)
	return srv.Stop()
}

// StopAll 停止所有监听器
func (m *Manager) StopAll() {
	m.mu.Lock()
	servers := m.servers
	m.servers = make(map[string]Server)
	m.mu.Unlock()
	for id, srv := range servers {
		if err := srv.Stop(); err != nil {
			m.log.TunnelError(m.tunnelID, "停止监听器 "+id+" 失败: "+err.Error())
		}
	}
}

// Status 获取所有监听器状态
func (m *Manager) Status() map[string]ListenerStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]ListenerStatus, len(m.servers))
	for id, srv := range m.servers {
		out[id] = srv.Status()
	}
	return out
}
