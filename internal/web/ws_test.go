package web

import (
	"context"
	"net/http"
	"net/http/httptest"
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
