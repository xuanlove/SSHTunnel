package proxy

import (
	"sync"
	"time"
)

// AccessRecord 一条结构化访问记录（仅在监听器开启「记录访问日志」时采集）
type AccessRecord struct {
	Time       time.Time `json:"time"`
	TunnelID   string    `json:"tunnel_id"`
	TunnelName string    `json:"tunnel_name"`
	ListenerID string    `json:"listener_id"`
	Protocol   string    `json:"protocol"`
	ListenPort int       `json:"listen_port"`
	Target     string    `json:"target"`
	Source     string    `json:"source"`
	Result     string    `json:"result"`
	DurationMs int64     `json:"duration_ms"`
	Denied     bool      `json:"denied"`
}

// AccessHistory 固定容量的访问历史环形缓冲（并发安全）。
// 由隧道管理器共享，使历史跨重连与隧道停止保留（重启后从持久化分片恢复）。
type AccessHistory struct {
	mu   sync.Mutex
	buf  []AccessRecord
	next int
	full bool
	sink func(AccessRecord) // 持久化钩子（nil 表示不落盘）
}

// NewAccessHistory 创建访问历史，capacity 为最大保留条数
func NewAccessHistory(capacity int) *AccessHistory {
	if capacity <= 0 {
		capacity = 2000
	}
	return &AccessHistory{buf: make([]AccessRecord, capacity)}
}

// Add 追加一条访问记录（超出容量时覆盖最旧记录；触发持久化钩子）
func (h *AccessHistory) Add(rec AccessRecord) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.pushLocked(rec)
	sink := h.sink
	h.mu.Unlock()
	if sink != nil {
		sink(rec)
	}
}

// SetSink 设置持久化钩子（每条新记录触发一次；应在服务启动前设置）
func (h *AccessHistory) SetSink(fn func(AccessRecord)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.sink = fn
	h.mu.Unlock()
}

// Restore 恢复历史记录（升序传入；不触发持久化钩子）
func (h *AccessHistory) Restore(recs []AccessRecord) {
	if h == nil {
		return
	}
	h.mu.Lock()
	for _, rec := range recs {
		h.pushLocked(rec)
	}
	h.mu.Unlock()
}

// pushLocked 环形写入（调用方需持有锁）
func (h *AccessHistory) pushLocked(rec AccessRecord) {
	h.buf[h.next] = rec
	h.next = (h.next + 1) % len(h.buf)
	if h.next == 0 {
		h.full = true
	}
}

// Recent 返回最近的访问记录（时间倒序，最多 limit 条）
func (h *AccessHistory) Recent(limit int) []AccessRecord {
	if h == nil {
		return []AccessRecord{}
	}
	if limit <= 0 {
		limit = 200
	}
	h.mu.Lock()
	n := h.next
	if h.full {
		n = len(h.buf)
	}
	if n > limit {
		n = limit
	}
	out := make([]AccessRecord, 0, n)
	idx := (h.next - 1 + len(h.buf)) % len(h.buf)
	for i := 0; i < n; i++ {
		out = append(out, h.buf[idx])
		idx = (idx - 1 + len(h.buf)) % len(h.buf)
	}
	h.mu.Unlock()
	return out
}

// RecentByTunnel 返回指定隧道最近的访问记录（时间倒序，最多 limit 条）
func (h *AccessHistory) RecentByTunnel(tunnelID string, limit int) []AccessRecord {
	if h == nil {
		return []AccessRecord{}
	}
	if limit <= 0 {
		limit = 500
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.next
	if h.full {
		n = len(h.buf)
	}
	out := make([]AccessRecord, 0, limit)
	idx := (h.next - 1 + len(h.buf)) % len(h.buf)
	for i := 0; i < n && len(out) < limit; i++ {
		rec := h.buf[idx]
		if rec.TunnelID == tunnelID {
			out = append(out, rec)
		}
		idx = (idx - 1 + len(h.buf)) % len(h.buf)
	}
	return out
}
