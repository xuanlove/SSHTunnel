package proxy

import (
	"fmt"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// ConnMeta 活动连接的元信息
type ConnMeta struct {
	TunnelID   string
	TunnelName string
	ListenerID string
	Protocol   string
	ListenPort int
	Target     string
}

// ConnSnapshot 单条活动连接快照（供状态页展示）
type ConnSnapshot struct {
	ID         string    `json:"id"`
	TunnelID   string    `json:"tunnel_id"`
	TunnelName string    `json:"tunnel_name"`
	ListenerID string    `json:"listener_id"`
	Protocol   string    `json:"protocol"`
	ListenPort int       `json:"listen_port"`
	Source     string    `json:"source"`
	Target     string    `json:"target"`
	StartedAt  time.Time `json:"started_at"`
	BytesSent  int64     `json:"bytes_sent"`
	BytesRecv  int64     `json:"bytes_recv"`
}

// ConnEntry 活动连接的计数句柄，桥接时按方向实时累加。
// 方法均对 nil 实例安全，调用方无需判空。
type ConnEntry struct {
	id        string
	meta      ConnMeta
	source    string
	startedAt time.Time
	sent      atomic.Int64
	recv      atomic.Int64
}

// AddSent 累加上行字节（客户端 -> 目标）
func (e *ConnEntry) AddSent(n int64) {
	if e != nil && n > 0 {
		e.sent.Add(n)
	}
}

// AddRecv 累加下行字节（目标 -> 客户端）
func (e *ConnEntry) AddRecv(n int64) {
	if e != nil && n > 0 {
		e.recv.Add(n)
	}
}

// ConnRegistry 活动连接注册表（进程内共享，所有隧道与监听器共用）
type ConnRegistry struct {
	mu    sync.RWMutex
	conns map[string]*ConnEntry
	seq   atomic.Uint64
}

// NewConnRegistry 创建活动连接注册表
func NewConnRegistry() *ConnRegistry {
	return &ConnRegistry{conns: make(map[string]*ConnEntry)}
}

// Open 注册一条活动连接；来源地址从 conn 自动提取
func (r *ConnRegistry) Open(conn net.Conn, meta ConnMeta) *ConnEntry {
	if r == nil {
		return nil
	}
	src := ""
	if ip := remoteIP(conn); ip != nil {
		src = ip.String()
	}
	e := &ConnEntry{
		id:        fmt.Sprintf("c%d", r.seq.Add(1)),
		meta:      meta,
		source:    src,
		startedAt: time.Now(),
	}
	r.mu.Lock()
	r.conns[e.id] = e
	r.mu.Unlock()
	return e
}

// Close 注销一条活动连接
func (r *ConnRegistry) Close(e *ConnEntry) {
	if r == nil || e == nil {
		return
	}
	r.mu.Lock()
	delete(r.conns, e.id)
	r.mu.Unlock()
}

// RemoveTunnel 移除指定隧道的全部连接（隧道停止时清理残留）
func (r *ConnRegistry) RemoveTunnel(tunnelID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	for id, e := range r.conns {
		if e.meta.TunnelID == tunnelID {
			delete(r.conns, id)
		}
	}
	r.mu.Unlock()
}

// Snapshot 返回当前活动连接（按建立时间升序）
func (r *ConnRegistry) Snapshot() []ConnSnapshot {
	if r == nil {
		return []ConnSnapshot{}
	}
	r.mu.RLock()
	out := make([]ConnSnapshot, 0, len(r.conns))
	for _, e := range r.conns {
		out = append(out, ConnSnapshot{
			ID:         e.id,
			TunnelID:   e.meta.TunnelID,
			TunnelName: e.meta.TunnelName,
			ListenerID: e.meta.ListenerID,
			Protocol:   e.meta.Protocol,
			ListenPort: e.meta.ListenPort,
			Source:     e.source,
			Target:     e.meta.Target,
			StartedAt:  e.startedAt,
			BytesSent:  e.sent.Load(),
			BytesRecv:  e.recv.Load(),
		})
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out
}
