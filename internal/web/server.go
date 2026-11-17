package web

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed all:assets
var assetsFS embed.FS

// Config WEB 服务配置
type Config struct {
	Host      string // 监听地址
	Port      int    // 监听端口
	JWTSecret []byte // JWT 签名密钥
	TLSCert   string // TLS 证书路径
	TLSKey    string // TLS 私钥路径
}

// Server WEB API 服务器
type Server struct {
	cfg     Config
	handler *Handler
	srv     *http.Server
}

// NewServer 创建 WEB 服务器
func NewServer(cfg Config, handler *Handler) *Server {
	return &Server{
		cfg:     cfg,
		handler: handler,
	}
}

// Start 启动 WEB 服务器（阻塞）
func (s *Server) Start() error {
	mux := s.buildRoutes()

	addr := s.cfg.Host + ":" + strconv.Itoa(s.cfg.Port)
	s.srv = &http.Server{
		Addr:    addr,
		Handler: mux,
		// 超时设置
		ReadHeaderTimeout: 10 * time.Second,
		// WebSocket 需要长连接，不设 ReadTimeout
	}

	// 安全警告
	if !s.handler.AuthEnabled() && (s.cfg.Host == "0.0.0.0" || s.cfg.Host == "::") {
		log.Printf("⚠️  警告：WEB 面板以无密码模式监听所有网卡 %s，任何能访问本机端口 %d 的设备均可控制隧道；可在「系统设置」中开启访问密码，或确认网络环境可信", s.cfg.Host, s.cfg.Port)
	}

	log.Printf("WEB 面板启动: http://%s", addr)

	if s.cfg.TLSCert != "" && s.cfg.TLSKey != "" {
		log.Printf("启用 HTTPS: 证书 %s", s.cfg.TLSCert)
		return s.srv.ListenAndServeTLS(s.cfg.TLSCert, s.cfg.TLSKey)
	}
	return s.srv.ListenAndServe()
}

// Shutdown 优雅关闭
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv != nil {
		return s.srv.Shutdown(ctx)
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
	protected := http.StripPrefix("/api", s.handler.apiRouter())
	authed := authMiddleware(s.handler.AuthEnabled, s.cfg.JWTSecret)(protected)
	mux.Handle("/api/", authed)

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
	tlsEnabled := s.cfg.TLSCert != "" && s.cfg.TLSKey != ""
	writeJSON(w, 0, "success", map[string]interface{}{
		"auth_enabled": s.handler.AuthEnabled(),
		"tls_enabled":  tlsEnabled,
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
		case code >= 40000:
			httpStatus = 400
		case code >= 40100:
			httpStatus = 401
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
