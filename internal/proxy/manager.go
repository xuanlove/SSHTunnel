package proxy

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
)

// Manager 多端口多协议代理管理器，绑定到单条 SSH 隧道
type Manager struct {
	mu         sync.RWMutex
	servers    map[string]Server // listenerID -> server
	stats      map[string]*TrafficStats
	meta       map[string]ListenerStats // listenerID -> 监听器元信息（断线期间仍可输出累计统计）
	dialer     Dialer
	log        *logger.Logger
	tunnelID   string
	tunnelName string
	conns      *ConnRegistry  // 活动连接注册表（可跨隧道共享）
	history    *AccessHistory // 结构化访问历史（可跨隧道共享）
}

// NewManager 创建代理管理器
func NewManager(dialer Dialer, log *logger.Logger, tunnelID, tunnelName string) *Manager {
	return &Manager{
		servers:    make(map[string]Server),
		stats:      make(map[string]*TrafficStats),
		meta:       make(map[string]ListenerStats),
		dialer:     dialer,
		log:        log,
		tunnelID:   tunnelID,
		tunnelName: tunnelName,
	}
}

// SetTracker 注入连接与访问历史采集上下文（连接统计与访问历史可在多个隧道间共享）
func (m *Manager) SetTracker(conns *ConnRegistry, history *AccessHistory) {
	m.mu.Lock()
	m.conns = conns
	m.history = history
	m.mu.Unlock()
}

// SetDialer 更新出站拨号器（隧道重连后需指向新的 SSH 连接）
func (m *Manager) SetDialer(d Dialer) {
	m.mu.Lock()
	m.dialer = d
	m.mu.Unlock()
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
	// 同一监听器 ID 复用统计对象：隧道重连重建监听器后「累计」数据不清零
	st, ok := m.stats[lc.ID]
	if !ok {
		st = &TrafficStats{}
		m.stats[lc.ID] = st
	}
	tracker := &Tracker{
		TunnelID:   m.tunnelID,
		TunnelName: m.tunnelName,
		Conns:      m.conns,
		History:    m.history,
	}
	srv, err := buildServerWithStats(lc, m.dialer, func(level, msg string) {
		switch level {
		case "error":
			m.log.TunnelError(m.tunnelID, "["+string(lc.Protocol)+":"+fmt.Sprintf("%d", lc.ListenPort)+"] "+msg)
		case "warn":
			m.log.TunnelWarn(m.tunnelID, "["+string(lc.Protocol)+":"+fmt.Sprintf("%d", lc.ListenPort)+"] "+msg)
		default:
			m.log.TunnelInfo(m.tunnelID, "["+string(lc.Protocol)+":"+fmt.Sprintf("%d", lc.ListenPort)+"] "+msg)
		}
	}, st, tracker)
	if err != nil {
		return err
	}
	// 构建成功后才登记元信息：构建失败（证书/参数错误）的监听器不应出现在统计中
	m.meta[lc.ID] = ListenerStats{
		ID:         lc.ID,
		Protocol:   string(lc.Protocol),
		ListenPort: lc.ListenPort,
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

// Stats 获取所有已配置监听器的连接与流量统计（按监听端口排序）。
// 即使监听器因隧道重连暂时停止，也返回其累计统计，避免界面在重连窗口归零。
func (m *Manager) Stats() []ListenerStats {
	m.mu.RLock()
	out := make([]ListenerStats, 0, len(m.meta))
	for id, meta := range m.meta {
		st := meta
		if c, ok := m.stats[id]; ok {
			st.ActiveConns, st.TotalConns, st.BytesSent, st.BytesRecv = c.Snapshot()
		}
		out = append(out, st)
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ListenPort < out[j].ListenPort })
	return out
}
