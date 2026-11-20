package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// WSMessage WebSocket 推送消息
type WSMessage struct {
	Type string      `json:"type"` // log | status
	Data interface{} `json:"data"`
}

// WebSocket 推送参数
const (
	wsSendBuffer   = 64              // 每连接发送队列长度
	wsMaxClients   = 64              // 最大并发连接数
	wsWriteTimeout = 5 * time.Second // 单次写超时
)

// wsClient 单个 WebSocket 连接（独立发送队列，避免慢客户端阻塞广播）
type wsClient struct {
	conn   *websocket.Conn
	send   chan []byte
	closed bool
}

// Hub 管理所有 WebSocket 连接，广播消息
type Hub struct {
	mu      sync.Mutex
	clients map[*wsClient]struct{}
}

// NewHub 创建 hub
func NewHub() *Hub {
	return &Hub{
		clients: make(map[*wsClient]struct{}),
	}
}

// Broadcast 向所有客户端广播消息；队列已满的慢客户端会被断开，不阻塞其他客户端
func (h *Hub) Broadcast(msg WSMessage) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.Lock()
	for c := range h.clients {
		select {
		case c.send <- data:
		default:
			h.removeLocked(c)
		}
	}
	h.mu.Unlock()
}

// addClient 注册连接并启动写协程；超过连接上限时拒绝
func (h *Hub) addClient(c *websocket.Conn) *wsClient {
	h.mu.Lock()
	if len(h.clients) >= wsMaxClients {
		h.mu.Unlock()
		_ = c.Close(websocket.StatusTryAgainLater, "too many connections")
		return nil
	}
	client := &wsClient{conn: c, send: make(chan []byte, wsSendBuffer)}
	h.clients[client] = struct{}{}
	h.mu.Unlock()
	go client.writeLoop(h)
	return client
}

// writeLoop 串行发送消息并执行心跳，避免并发写
func (c *wsClient) writeLoop(h *Hub) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
			err := c.conn.Write(ctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				h.removeClient(c)
				return
			}
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := c.conn.Ping(ctx)
			cancel()
			if err != nil {
				h.removeClient(c)
				return
			}
		}
	}
}

func (h *Hub) removeClient(c *wsClient) {
	h.mu.Lock()
	h.removeLocked(c)
	h.mu.Unlock()
}

// removeLocked 移除连接（调用方需持有锁）
func (h *Hub) removeLocked(c *wsClient) {
	if c.closed {
		return
	}
	c.closed = true
	delete(h.clients, c)
	close(c.send)
	// 关闭连接可能阻塞，放到独立 goroutine，避免持锁等待
	go func() { _ = c.conn.Close(websocket.StatusNormalClosure, "") }()
}

// handleLogStream WebSocket 日志流 /api/logs/stream
//
// 清理策略：使用单一 cleanup 闭包（sync.Once 保护）统一回收资源，
// 避免读循环、心跳 goroutine 与广播路径并发 removeClient 造成的不一致。
// 任意退出路径（读错误、Ping 失败、请求上下文取消）均触发同一清理逻辑：
// 取消派生 context（使心跳 goroutine 立即退出）并从 hub 移除连接。
func (h *Handler) handleLogStream(w http.ResponseWriter, r *http.Request) {
	// 防止跨站 WebSocket 劫持：浏览器携带 Origin 时必须与请求 Host 同源
	// （非浏览器客户端通常不带 Origin，放行）
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	client := h.hub.addClient(c)
	if client == nil {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var once sync.Once
	cleanup := func() {
		once.Do(func() {
			cancel()                   // 通知读循环退出
			h.hub.removeClient(client) // 幂等：已移除则跳过
		})
	}
	defer cleanup()

	// 阻塞读取（忽略客户端消息）；心跳由写协程串行执行
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
