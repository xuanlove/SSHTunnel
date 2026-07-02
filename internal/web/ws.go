package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// WSMessage WebSocket 推送消息
type WSMessage struct {
	Type string      `json:"type"` // log | status
	Data interface{} `json:"data"`
}

// Hub 管理所有 WebSocket 连接，广播消息
type Hub struct {
	mu      sync.RWMutex
	clients map[*websocket.Conn]bool
}

// NewHub 创建 hub
func NewHub() *Hub {
	return &Hub{
		clients: make(map[*websocket.Conn]bool),
	}
}

// Broadcast 向所有客户端广播消息
func (h *Hub) Broadcast(msg WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	clients := make([]*websocket.Conn, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := c.Write(ctx, websocket.MessageText, data)
		cancel()
		if err != nil {
			h.removeClient(c)
		}
	}
}

func (h *Hub) addClient(c *websocket.Conn) {
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
}

func (h *Hub) removeClient(c *websocket.Conn) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		_ = c.Close(websocket.StatusNormalClosure, "")
		delete(h.clients, c)
	}
	h.mu.Unlock()
}

// handleLogStream WebSocket 日志流 /api/logs/stream
//
// 清理策略：使用单一 cleanup 闭包（sync.Once 保护）统一回收资源，
// 避免读循环、心跳 goroutine 与广播路径并发 removeClient 造成的不一致。
// 任意退出路径（读错误、Ping 失败、请求上下文取消）均触发同一清理逻辑：
// 取消派生 context（使心跳 goroutine 立即退出）并从 hub 移除连接。
func (h *Handler) handleLogStream(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	h.hub.addClient(c)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			cancel()              // 通知心跳 goroutine 退出
			h.hub.removeClient(c) // 幂等：已移除则跳过
		})
	}
	defer cleanup()

	// 心跳：周期性 Ping，失败或 context 取消即退出
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := c.Ping(ctx); err != nil {
					cleanup()
					return
				}
			}
		}
	}()

	// 阻塞读取（忽略客户端消息）；读取错误触发 defer cleanup
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return
		}
	}
}

// handleStatusStream WebSocket 状态流 /api/tunnels/status/stream
func (h *Handler) handleStatusStream(w http.ResponseWriter, r *http.Request) {
	// 复用日志流的连接管理逻辑
	h.handleLogStream(w, r)
}
