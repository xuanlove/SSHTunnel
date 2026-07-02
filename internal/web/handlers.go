package web

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
	"sshtunnel/internal/port"
	"sshtunnel/internal/proxy"
	"sshtunnel/internal/sshclient"
	"sshtunnel/internal/tunnel"
	"sshtunnel/internal/updater"
)

// Handler API 处理器，持有共享业务层引用
type Handler struct {
	cfgMgr    *config.Manager
	tunnelMgr *tunnel.Manager
	log       *logger.Logger
	jwtSecret []byte
	authUser  string
	authPass  string // 明文密码（启动时传入）
	hub       *Hub
	version   string // 当前版本号（构建时注入）
	commit    string // git commit
	variant   string // 构建变体：web 或 desktop
	repo      string // GitHub 仓库，用于版本检查
}

// NewHandler 创建 API 处理器
func NewHandler(cfgMgr *config.Manager, tunnelMgr *tunnel.Manager, log *logger.Logger,
	jwtSecret []byte, authUser, authPass string, hub *Hub) *Handler {
	return &Handler{
		cfgMgr:    cfgMgr,
		tunnelMgr: tunnelMgr,
		log:       log,
		jwtSecret: jwtSecret,
		authUser:  authUser,
		authPass:  authPass,
		hub:       hub,
	}
}

// WithVersion 注入版本信息（构建时由 main.go 调用）。
func (h *Handler) WithVersion(version, commit, variant, repo string) *Handler {
	h.version = version
	h.commit = commit
	h.variant = variant
	h.repo = repo
	return h
}

// JWTSecret 返回 JWT 密钥（供 main.go 传递给 Server）
func (h *Handler) JWTSecret() []byte {
	return h.jwtSecret
}

// apiRouter 构建 /api 子路由
func (h *Handler) apiRouter() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/configs", h.handleConfigs)
	mux.HandleFunc("/configs/", h.handleConfigByID)
	mux.HandleFunc("/tunnels/start-all", h.handleStartAll)
	mux.HandleFunc("/tunnels/stop-all", h.handleStopAll)
	mux.HandleFunc("/tunnels/status", h.handleStatus)
	mux.HandleFunc("/tunnels/status/stream", h.handleStatusStream) // WebSocket
	mux.HandleFunc("/logs", h.handleLogs)
	mux.HandleFunc("/logs/stream", h.handleLogStream) // WebSocket
	mux.HandleFunc("/port/check", h.handlePortCheck)
	mux.HandleFunc("/cert/generate", h.handleCertGenerate)
	mux.HandleFunc("/hopchain/parse", h.handleHopChainParse)
	mux.HandleFunc("/tunnel/test", h.handleTunnelTest)
	mux.HandleFunc("/version", h.handleVersion)
	mux.HandleFunc("/version/check", h.handleVersionCheck)

	return mux
}

// handleVersion 返回当前二进制的版本信息。
func (h *Handler) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "方法不允许")
		return
	}
	writeJSON(w, 0, "success", map[string]string{
		"version": h.version,
		"commit":  h.commit,
		"variant": h.variant,
		"repo":    h.repo,
	})
}

// handleVersionCheck 查询 GitHub Release 最新版本并与当前比较。
func (h *Handler) handleVersionCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "方法不允许")
		return
	}
	latest, updateAvailable, rel, err := updater.CheckUpdate(h.version, h.repo)
	if err != nil {
		h.log.Warnf("版本检查失败: %v", err)
		writeError(w, 50002, "版本检查失败: "+err.Error())
		return
	}
	resp := map[string]interface{}{
		"current":          h.version,
		"latest":           latest,
		"variant":          h.variant,
		"update_available": updateAvailable,
	}
	if rel != nil {
		resp["release_url"] = rel.HTMLURL
		if u, err := rel.AssetForPlatform("", "", h.variant); err == nil {
			resp["download_url"] = u
		}
	}
	writeJSON(w, 0, "success", resp)
}

// handleLogin 登录接口
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if req.Username != h.authUser || req.Password != h.authPass {
		h.log.Infof("登录失败: 用户名或密码错误 (user=%s)", req.Username)
		writeError(w, 40001, "用户名或密码错误")
		return
	}
	h.log.Infof("用户登录成功: %s", req.Username)
	token, expiresAt, err := generateJWT(req.Username, h.jwtSecret)
	if err != nil {
		writeError(w, 500, "生成 Token 失败")
		return
	}
	writeJSON(w, 0, "success", map[string]interface{}{
		"token":      token,
		"expires_at": expiresAt,
	})
}

// handleConfigs 配置列表 CRUD
func (h *Handler) handleConfigs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 0, "success", h.cfgMgr.List())
	case http.MethodPost:
		var cfg config.TunnelConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeError(w, 400, "请求格式错误")
			return
		}
		if err := h.cfgMgr.Save(&cfg); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		h.log.Infof("配置已创建: %s (ID: %s)", cfg.Name, cfg.ID)
		writeJSON(w, 0, "success", cfg)
	default:
		writeError(w, 405, "方法不允许")
	}
}

// handleConfigByID 单个配置操作
func (h *Handler) handleConfigByID(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	// /configs/{id} 或 /configs/{id}/start 或 /configs/{id}/stop
	parts := splitPath(path)
	if len(parts) < 2 {
		writeError(w, 400, "缺少配置 ID")
		return
	}
	id := parts[1]
	action := ""
	if len(parts) >= 3 {
		action = parts[2]
	}

	switch {
	case action == "" && r.Method == http.MethodPut:
		var cfg config.TunnelConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			writeError(w, 400, "请求格式错误")
			return
		}
		cfg.ID = id
		if err := h.cfgMgr.Save(&cfg); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		h.log.Infof("配置已更新: %s (ID: %s)", cfg.Name, cfg.ID)
		writeJSON(w, 0, "success", cfg)

	case action == "" && r.Method == http.MethodDelete:
		if err := h.cfgMgr.Delete(id); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		h.log.Infof("配置已删除: ID: %s", id)
		writeJSON(w, 0, "success", nil)

	case action == "start" && r.Method == http.MethodPost:
		cfg, ok := h.cfgMgr.Get(id)
		if !ok {
			writeError(w, 404, "配置不存在")
			return
		}
		h.cfgMgr.SetStatus(id, config.StatusStarting)
		h.log.Infof("正在启动隧道: %s (ID: %s)", cfg.Name, id)
		if err := h.tunnelMgr.Start(cfg); err != nil {
			h.cfgMgr.SetStatus(id, config.StatusError)
			h.log.Errorf("隧道启动失败: %s - %v", cfg.Name, err)
			writeError(w, 500, err.Error())
			return
		}
		h.cfgMgr.SetStatus(id, config.StatusRunning)
		h.log.Infof("隧道已启动: %s (ID: %s)", cfg.Name, id)
		writeJSON(w, 0, "success", nil)

	case action == "stop" && r.Method == http.MethodPost:
		if err := h.tunnelMgr.Stop(id); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		h.cfgMgr.SetStatus(id, config.StatusStopped)
		h.log.Infof("隧道已停止: ID: %s", id)
		writeJSON(w, 0, "success", nil)

	default:
		writeError(w, 404, "未知操作")
	}
}

// handleStartAll 批量启动
func (h *Handler) handleStartAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	h.log.Info("批量启动所有隧道")
	for _, cfg := range h.cfgMgr.List() {
		if !h.tunnelMgr.IsRunning(cfg.ID) {
			if err := h.tunnelMgr.Start(&cfg); err != nil {
				h.cfgMgr.SetStatus(cfg.ID, config.StatusError)
				h.log.Errorf("批量启动失败: %s - %v", cfg.Name, err)
				writeError(w, 500, "启动 "+cfg.Name+" 失败: "+err.Error())
				return
			}
			h.cfgMgr.SetStatus(cfg.ID, config.StatusRunning)
		}
	}
	writeJSON(w, 0, "success", nil)
}

// handleStopAll 批量停止
func (h *Handler) handleStopAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	h.log.Info("批量停止所有隧道")
	h.tunnelMgr.StopAll()
	for _, cfg := range h.cfgMgr.List() {
		h.cfgMgr.SetStatus(cfg.ID, config.StatusStopped)
	}
	writeJSON(w, 0, "success", nil)
}

// handleStatus 所有隧道状态
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	statuses := make(map[string]string)
	for _, cfg := range h.cfgMgr.List() {
		if h.tunnelMgr.IsRunning(cfg.ID) {
			statuses[cfg.ID] = "running"
		} else {
			statuses[cfg.ID] = string(cfg.Status)
		}
	}
	writeJSON(w, 0, "success", statuses)
}

// handleLogs 获取最近日志
func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	limit := 500
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	writeJSON(w, 0, "success", h.log.Recent(limit))
}

// handlePortCheck 端口可用性检测
func (h *Handler) handlePortCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	var req struct {
		Port int `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	writeJSON(w, 0, "success", map[string]bool{
		"available": port.IsAvailable(req.Port),
	})
}

// handleCertGenerate 生成自签证书
func (h *Handler) handleCertGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	certPath, keyPath, err := proxy.GenerateSelfSignedCert()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 0, "success", map[string]string{
		"cert_path": certPath,
		"key_path":  keyPath,
	})
}

// handleHopChainParse 解析简写跳板链
func (h *Handler) handleHopChainParse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, 400, "读取请求体失败")
		return
	}
	var req struct {
		Chain string `json:"chain"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	hops := sshclient.ParseHopChain(req.Chain)
	out := make([]config.HopConfig, len(hops))
	for i, hop := range hops {
		out[i] = config.HopConfig{
			User: hop.User,
			Host: hop.Host,
			Port: hop.Port,
		}
	}
	writeJSON(w, 0, "success", out)
}

// handleTunnelTest 测试隧道连接
func (h *Handler) handleTunnelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	var cfg config.TunnelConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	hops := make([]sshclient.Hop, len(cfg.HopChain))
	for i, hop := range cfg.HopChain {
		hops[i] = sshclient.Hop{
			User:       hop.User,
			Host:       hop.Host,
			Port:       hop.Port,
			AuthType:   hop.AuthType,
			Password:   hop.Password,
			KeyContent: hop.KeyContent,
			Passphrase: hop.Passphrase,
		}
	}
	client, err := sshclient.Dial(hops)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	client.Close()
	writeJSON(w, 0, "success", nil)
}

// splitPath 拆分 URL 路径（忽略空段）
func splitPath(path string) []string {
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}
