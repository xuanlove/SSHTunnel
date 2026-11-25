package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sshtunnel/internal/logger"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestHubSlowClientDoesNotBlock 慢客户端不应阻塞其他客户端接收广播
func TestHubSlowClientDoesNotBlock(t *testing.T) {
	hub := NewHub()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		client := hub.addClient(c)
		if client == nil {
			return
		}
		defer hub.removeClient(client)
		ctx := r.Context()
		for {
			if _, _, err := c.Read(ctx); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	fast, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("连接客户端 A 失败: %v", err)
	}
	defer fast.Close(websocket.StatusNormalClosure, "")

	slow, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("连接客户端 B 失败: %v", err)
	}
	defer slow.Close(websocket.StatusNormalClosure, "")

	// 等待两个连接完成注册
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		n := len(hub.clients)
		hub.mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 客户端 A 正常读取；客户端 B 不读取（模拟慢客户端）
	received := make(chan struct{}, 1)
	go func() {
		for {
			_, data, err := fast.Read(ctx)
			if err != nil {
				return
			}
			if len(data) > 0 {
				select {
				case received <- struct{}{}:
				default:
				}
			}
		}
	}()

	start := time.Now()
	for i := 0; i < 300; i++ {
		hub.Broadcast(WSMessage{Type: "log", Data: strings.Repeat("x", 4096)})
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("广播被慢客户端阻塞: 耗时 %v", elapsed)
	}

	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Errorf("正常客户端未收到任何广播")
	}
}

func TestPasswordChangeRevokesJWTAndWebSocket(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), nil, "admin", "pass123", NewHub())
	router := NewServer(Config{Host: "127.0.0.1"}, h).buildRoutes()
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	oldToken, _, err := generateJWT("admin", h.auth.Secret())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	headers := http.Header{"Authorization": {"Bearer " + oldToken}}
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/logs/stream",
		&websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()

	req := httptest.NewRequest(http.MethodPut, "/api/auth/settings",
		strings.NewReader(`{"enabled":true,"username":"admin","password":"new-password"}`))
	req.Header = headers
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("修改密码失败: %d", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/auth/settings", nil)
	req.Header = headers
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("旧令牌应被拒绝，状态码为 %d", w.Code)
	}
	if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("旧 WebSocket 应在改密后关闭: %v", err)
	}
}
