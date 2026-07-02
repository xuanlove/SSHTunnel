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
func (h *Handler) handleLogStream(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	h.hub.addClient(c)

	// 心跳与清理
	ctx := r.Context()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				h.hub.removeClient(c)
				return
			case <-ticker.C:
				if err := c.Ping(ctx); err != nil {
					h.hub.removeClient(c)
					return
				}
			}
		}
	}()

	// 阻塞读取（忽略客户端消息）
	for {
		_, _, err := c.Read(ctx)
		if err != nil {
			h.hub.removeClient(c)
			return
		}
	}
}

// handleStatusStream WebSocket 状态流 /api/tunnels/status/stream
func (h *Handler) handleStatusStream(w http.ResponseWriter, r *http.Request) {
	// 复用日志流的连接管理逻辑
	h.handleLogStream(w, r)
}
