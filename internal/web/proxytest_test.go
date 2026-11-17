package web

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
	"sshtunnel/internal/port"
	"sshtunnel/internal/proxy"
)

// directDialer 直连 Dialer（测试用，等价于无隧道时的直连）
type directDialer struct{}

func (directDialer) Dial(network, address string) (net.Conn, error) {
	return net.DialTimeout(network, address, 3*time.Second)
}

// startTestSOCKS5 启动本地 SOCKS5 代理并等待监听就绪
func startTestSOCKS5(t *testing.T) (int, func()) {
	t.Helper()
	p := port.FindAvailable(24000)
	if p == 0 {
		t.Fatalf("无法找到空闲端口")
	}
	srv, err := proxy.BuildServer(config.ProxyListener{
		ID:         "web-test",
		Protocol:   config.ProxySOCKS5,
		ListenPort: p,
	}, directDialer{}, nil)
	if err != nil {
		t.Fatalf("构建 SOCKS5 失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Start(ctx)

	addr := "127.0.0.1:" + strconv.Itoa(p)
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SOCKS5 未就绪: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return p, func() { cancel(); srv.Stop() }
}

// TestHandleProxyTestSuccess 验证完整链路：handler → probe → SOCKS5 → 目标，返回出口 IP
func TestHandleProxyTestSuccess(t *testing.T) {
	// 目标服务器返回固定 IP 文本
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "203.0.113.88\n")
	}))
	defer target.Close()

	proxyPort, stop := startTestSOCKS5(t)
	defer stop()

	h := NewHandler(nil, nil, logger.NewTest(), []byte("secret"), "", "", nil)
	body := `{"protocol":"socks5","port":` + strconv.Itoa(proxyPort) + `,"target":"http://` + target.Listener.Addr().String() + `/ip"}`
	w := httptest.NewRecorder()
	h.handleProxyTest(w, httptest.NewRequest("POST", "/proxy/test", strings.NewReader(body)))

	if w.Code != 200 {
		t.Fatalf("期望 200，实际 %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			IP string `json:"ip"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.IP != "203.0.113.88" {
		t.Errorf("出口 IP = %q, want 203.0.113.88", resp.Data.IP)
	}
}

// TestHandleProxyTestBadPort 验证端口校验
func TestHandleProxyTestBadPort(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), []byte("secret"), "", "", nil)
	w := httptest.NewRecorder()
	h.handleProxyTest(w, httptest.NewRequest("POST", "/proxy/test",
		strings.NewReader(`{"protocol":"socks5","port":0}`)))
	if w.Code != 400 {
		t.Errorf("端口 0 期望 400，实际 %d", w.Code)
	}
}

// TestHandleProxyTestUnreachable 验证代理未监听时返回业务错误
func TestHandleProxyTestUnreachable(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), []byte("secret"), "", "", nil)
	p := port.FindAvailable(25000)
	body := `{"protocol":"socks5","port":` + strconv.Itoa(p) + `}`
	w := httptest.NewRecorder()
	h.handleProxyTest(w, httptest.NewRequest("POST", "/proxy/test", strings.NewReader(body)))
	if w.Code != 500 {
		t.Errorf("连接失败期望 500，实际 %d: %s", w.Code, w.Body.String())
	}
}
