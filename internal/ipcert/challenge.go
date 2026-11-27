package ipcert

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// challengeServer 临时的 HTTP-01 应答服务（绑定 0.0.0.0:<port>）
type challengeServer struct {
	mu     sync.RWMutex
	tokens map[string]string
	srv    *http.Server
	ln     net.Listener
}

func startChallengeServer(port int) (*challengeServer, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("启动 http-01 验证服务失败（端口 %d 需公网可达、未被占用，且当前进程有绑定权限——root 或 CAP_NET_BIND_SERVICE）: %w", port, err)
	}
	cs := &challengeServer{tokens: make(map[string]string), ln: ln}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/acme-challenge/", cs.serve)
	cs.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = cs.srv.Serve(ln)
	}()
	return cs, nil
}

func (c *challengeServer) serve(w http.ResponseWriter, r *http.Request) {
	const prefix = "/.well-known/acme-challenge/"
	token := strings.TrimPrefix(r.URL.Path, prefix)
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(w, r)
		return
	}
	c.mu.RLock()
	val, ok := c.tokens[token]
	c.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(val))
}

func (c *challengeServer) setToken(token, keyAuth string) {
	c.mu.Lock()
	c.tokens[token] = keyAuth
	c.mu.Unlock()
}

func (c *challengeServer) Close() error {
	if c.srv != nil {
		_ = c.srv.Close()
	}
	return nil
}
