package web

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
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
	Host string // 监听地址
	Port int    // 监听端口
}

// Server WEB API 服务器
type Server struct {
	cfg     Config
	handler *Handler

	mu       sync.Mutex
	srv      *http.Server
	closing  bool
	reloadCh chan struct{}
}

// NewServer 创建 WEB 服务器
func NewServer(cfg Config, handler *Handler) *Server {
	cfg.Host = strings.TrimSuffix(strings.TrimPrefix(cfg.Host, "["), "]")
	if strings.EqualFold(cfg.Host, "localhost") {
		cfg.Host = "127.0.0.1"
	}
	handler.authRequired = !net.ParseIP(cfg.Host).IsLoopback()
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
	if err := s.handler.auth.InitSecret(); err != nil {
		return err
	}
	if s.handler.authRequired && !s.handler.AuthEnabled() {
		return errors.New("非回环地址监听时必须启用 WEB 访问密码")
	}
	tlsConfig, err := s.handler.tls.listenerConfig()
	if err != nil {
		return err
	}
	mux := s.buildRoutes()
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))

	for {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		tlsOn := tlsConfig != nil
		if tlsOn {
			ln = tls.NewListener(ln, tlsConfig)
		}
		s.handler.tls.SetActive(tlsOn)
		if tlsOn {
			log.Printf("WEB 面板启动: https://%s", ln.Addr())
		} else {
			log.Printf("WEB 面板启动: http://%s", ln.Addr())
		}

		srv := &http.Server{
			Addr:              ln.Addr().String(),
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
			// WebSocket 需要长连接，不设 ReadTimeout
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			_ = ln.Close()
			s.handler.tls.SetActive(false)
			return nil
		}
		s.srv = srv
		s.mu.Unlock()

		errCh := make(chan error, 1)
		go func() { errCh <- srv.Serve(ln) }()

	reload:
		for {
			select {
			case err := <-errCh:
				s.handler.tls.SetActive(false)
				if errors.Is(err, http.ErrServerClosed) {
					return nil
				}
				return err
			case <-s.reloadCh:
				next, err := s.handler.tls.listenerConfig()
				if err != nil {
					log.Printf("保留当前 WEB 监听，HTTPS 设置重新加载失败: %v", err)
					continue
				}
				tlsConfig = next
				_ = srv.Close()
				<-errCh
				if s.handler.hub != nil {
					s.handler.hub.disconnectAll()
				}
				break reload
			}
		}
	}
}

// Shutdown 优雅关闭
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	srv := s.srv
	s.mu.Unlock()
	if s.handler.hub != nil {
		s.handler.hub.disconnectAll()
	}
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
	// 与其余 API 一致限制请求体大小，避免超大请求耗尽内存
	mux.Handle("/api/login", limitRequestBody(http.HandlerFunc(s.handler.handleLogin)))

	// 受保护 API（未启用密码时直接放行，启用后需 Token；支持运行时切换）
	protected := limitRequestBody(http.StripPrefix("/api", s.handler.apiRouter()))
	authed := authMiddleware(s.handler.auth.sessionState)(protected)
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
