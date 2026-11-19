package web

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed all:assets
var assetsFS embed.FS

// Config WEB 服务配置
type Config struct {
	Host      string // 监听地址
	Port      int    // 监听端口
	JWTSecret []byte // JWT 签名密钥
}

// Server WEB API 服务器
type Server struct {
	cfg     Config
	handler *Handler

	mu       sync.Mutex
	srv      *http.Server
	reloadCh chan struct{}
}

// NewServer 创建 WEB 服务器
func NewServer(cfg Config, handler *Handler) *Server {
	s := &Server{
		cfg:      cfg,
		handler:  handler,
		reloadCh: make(chan struct{}, 1),
	}
	// 允许系统设置中切换 TLS 后热重启监听
	handler.SetTLSReloader(func() {
		select {
		case s.reloadCh <- struct{}{}:
		default:
		}
	})
	return s
}

// Start 启动 WEB 服务器（阻塞）。
// TLS 设置变化时会在同一端口重启监听，无需重启进程。
func (s *Server) Start() error {
	mux := s.buildRoutes()
	addr := s.cfg.Host + ":" + strconv.Itoa(s.cfg.Port)

	// 安全警告
	if !s.handler.AuthEnabled() && (s.cfg.Host == "0.0.0.0" || s.cfg.Host == "::") {
		log.Printf("⚠️  警告：WEB 面板以无密码模式监听所有网卡 %s，任何能访问本机端口 %d 的设备均可控制隧道；可在「系统设置」中开启访问密码，或确认网络环境可信", s.cfg.Host, s.cfg.Port)
	}

	for {
		ln, tlsOn, err := s.listen(addr)
		if err != nil {
			return err
		}
		s.handler.tls.SetActive(tlsOn)
		if tlsOn {
			log.Printf("WEB 面板启动: https://%s", addr)
		} else {
			log.Printf("WEB 面板启动: http://%s", addr)
		}

		srv := &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			// WebSocket 需要长连接，不设 ReadTimeout
		}
		s.mu.Lock()
		s.srv = srv
		s.mu.Unlock()

		errCh := make(chan error, 1)
		go func() { errCh <- srv.Serve(ln) }()

		select {
		case err := <-errCh:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-s.reloadCh:
			// 强制关闭现有连接（含 WebSocket），以便在同一端口重新监听
			_ = srv.Close()
			<-errCh
			s.handler.tls.SetActive(false)
			log.Println("WEB 面板监听已重启（TLS 设置变更）")
		}
	}
}

// listen 按当前 TLS 设置创建监听器。
// 证书加载失败时：已启用访问密码则退回 HTTP 保证面板可修正配置；无密码则拒绝明文启动。
func (s *Server) listen(addr string) (net.Listener, bool, error) {
	certFile, keyFile, tlsEnabled := s.handler.tls.Effective()
	var cert tls.Certificate
	if tlsEnabled {
		c, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			if !s.handler.AuthEnabled() {
				return nil, false, fmt.Errorf("WEB 面板 HTTPS 证书加载失败且未启用访问密码，拒绝以明文启动: %w", err)
			}
			log.Printf("⚠️  WEB 面板 HTTPS 证书加载失败，已退回 HTTP（已启用访问密码）: %v", err)
			tlsEnabled = false
		} else {
			cert = c
		}
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, false, err
	}
	if !tlsEnabled {
		return ln, false, nil
	}
	log.Printf("已启用 HTTPS: 证书 %s", certFile)
	return tls.NewListener(ln, &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}), true, nil
}

// Shutdown 优雅关闭
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.srv
	s.mu.Unlock()
	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

// buildRoutes 构建路由树
func (s *Server) buildRoutes() *http.ServeMux {
	mux := http.NewServeMux()

	// 公开路由
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	// 登录接口始终注册；未开启访问密码时返回提示（兼容运行时动态开启）
	mux.HandleFunc("/api/login", s.handler.handleLogin)

	// 受保护 API（未启用密码时直接放行，启用后需 Token；支持运行时切换）
	protected := limitRequestBody(http.StripPrefix("/api", s.handler.apiRouter()))
	authed := authMiddleware(s.handler.AuthEnabled, s.cfg.JWTSecret)(protected)
	mux.Handle("/api/", authed)

	// 面板内部的证书工具接口（生成 / 解析）走登录鉴权，优先于下方 certd 的 /api/cert/ 前缀
	mux.Handle("/api/cert/generate", authed)
	mux.Handle("/api/cert/inspect", authed)

	// certd 等外部工具使用的证书部署 API：使用独立 Token 鉴权（不受 WEB 登录开关影响）
	certd := limitRequestBody(http.StripPrefix("/api", certdTokenMiddleware(s.handler.tls.DeployToken)(s.handler.certRouter())))
	mux.Handle("/api/cert/", certd)

	// 静态文件（前端 SPA）
	staticFS, _ := fs.Sub(assetsFS, "assets")
	fileServer := http.FileServer(http.FS(staticFS))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// 资源不存在
		if path != "/" && !fileExists(staticFS, path) {
			// 带扩展名的静态资源缺失（典型场景：升级后浏览器仍引用旧 hash 的 chunk）
			// 必须返回 404，不能回退成 index.html —— 否则浏览器拿 HTML 当 JS 解析，
			// 动态导入失败会让前端路由导航被中止，表现为"点击菜单没有任何反应"。
			if filepath.Ext(path) != "" {
				http.NotFound(w, r)
				return
			}
			// 前端路由（无扩展名）→ SPA fallback 到 index.html
			r.URL.Path = "/"
		}

		// 缓存策略：
		// - index.html（含 SPA fallback）禁止缓存，保证升级后能拿到新的资源引用
		// - 带内容 hash 的 assets 长期缓存，提升加载速度
		if r.URL.Path == "/" || strings.HasSuffix(r.URL.Path, ".html") {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})

	return mux
}

// handleAuthStatus 返回当前鉴权状态（动态）
func (s *Server) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 0, "success", map[string]interface{}{
		"auth_enabled": s.handler.AuthEnabled(),
		"tls_enabled":  s.handler.tls.Active(),
	})
}

// fileExists 检查静态文件系统中是否存在指定路径
func fileExists(fsys fs.FS, path string) bool {
	cleaned := strings.TrimPrefix(path, "/")
	if cleaned == "" {
		return false
	}
	_, err := fs.Stat(fsys, cleaned)
	return err == nil
}

// maxRequestBody 单个 API 请求体上限（证书/私钥/配置均远小于此值）
const maxRequestBody = 4 << 20

// limitRequestBody 限制请求体大小，避免超大请求耗尽内存
func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON 统一 JSON 响应
func writeJSON(w http.ResponseWriter, code int, message string, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	resp := map[string]interface{}{
		"code":    code,
		"message": message,
	}
	if data != nil {
		resp["data"] = data
	}
	json.NewEncoder(w).Encode(resp)
}

// writeError 写错误响应（同时设置 HTTP 状态码）
// code 同时作为业务码；若 code >= 1000 则作为业务码，HTTP status 映射为对应类别
func writeError(w http.ResponseWriter, code int, message string) {
	httpStatus := code
	if code >= 1000 {
		// 业务码映射到 HTTP status
		switch {
		case code >= 50000:
			httpStatus = 500
		case code >= 40100:
			httpStatus = 401
		case code >= 40000:
			httpStatus = 400
		default:
			httpStatus = 400
		}
	}
	// 必须在 WriteHeader 之前设置 Content-Type
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(httpStatus)
	resp := map[string]interface{}{
		"code":    code,
		"message": message,
	}
	json.NewEncoder(w).Encode(resp)
}
