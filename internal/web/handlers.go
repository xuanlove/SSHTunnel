package web

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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
	auth      *AuthStore // 运行时鉴权状态（可在系统设置中动态开启/关闭）
	tls       *TLSStore  // WEB 面板 HTTPS 设置（可在系统设置中动态开启/关闭）
	reloadTLS func()     // 由 WEB Server 注入，用于热重启监听
	hub       *Hub
	version   string // 当前版本号（构建时注入）
	commit    string // git commit
	variant   string // 构建变体：web 或 desktop
	repo      string // GitHub 仓库，用于版本检查
}

// NewHandler 创建 API 处理器。
// authUser 非空时（--auth 启动参数）初始化为已启用状态（仅内存）。
func NewHandler(cfgMgr *config.Manager, tunnelMgr *tunnel.Manager, log *logger.Logger,
	jwtSecret []byte, authUser, authPass string, hub *Hub) *Handler {
	h := &Handler{
		cfgMgr:    cfgMgr,
		tunnelMgr: tunnelMgr,
		log:       log,
		jwtSecret: jwtSecret,
		auth:      NewAuthStore(""),
		tls:       NewTLSStore(""),
		hub:       hub,
	}
	if authUser != "" {
		h.auth.initFromCLI(authUser, authPass)
	}
	return h
}

// UsePersistentAuth 加载持久化的访问密码设置（settings.json）。
// 若启动时提供了 --auth 参数，则以此覆盖（仅内存，不落盘）。
// WEB 模式由 main.go 调用；桌面模式无 WEB 服务，不调用。
func (h *Handler) UsePersistentAuth(cliUser, cliPass string) {
	h.auth = NewAuthStore(DefaultAuthStorePath())
	h.tls = NewTLSStore(DefaultWebTLSSettingsPath())
	// 使用持久化的 JWT 密钥：服务重启/升级后已签发的 Token 仍然有效
	h.jwtSecret = h.auth.Secret()
	if cliUser != "" {
		h.auth.initFromCLI(cliUser, cliPass)
		h.log.Info("已通过 --auth 启动参数启用访问密码（覆盖 settings.json）")
	}
	if h.auth.Enabled() {
		h.log.Infof("WEB 访问密码保护已启用 (user=%s)", h.auth.Username())
	}
	if s := h.tls.Settings(); s.Enabled {
		h.log.Infof("已启用 WEB 面板 HTTPS（证书 %s）", s.CertFile)
	}
	// 确保 certd 部署令牌已生成（首次启动时自动创建并持久化）
	h.tls.EnsureDeployToken()
}

// UseCLITLS 使用启动参数 --tls-cert/--tls-key 启用 WEB 面板 HTTPS（持久化）。
func (h *Handler) UseCLITLS(certFile, keyFile string) {
	if certFile == "" || keyFile == "" {
		return
	}
	if err := h.tls.EnableWithFiles(certFile, keyFile); err != nil {
		h.log.Warnf("保存启动参数指定的 TLS 证书失败: %v", err)
		return
	}
	h.log.Infof("已按启动参数启用 WEB 面板 HTTPS（证书 %s）", certFile)
}

// AuthEnabled 当前是否启用访问密码（供中间件动态判断）。
func (h *Handler) AuthEnabled() bool {
	return h.auth.Enabled()
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

// SetTLSReloader 注入 TLS 热重启回调（由 WEB Server 调用）。
func (h *Handler) SetTLSReloader(fn func()) {
	h.reloadTLS = fn
}

// TLSActive 当前监听是否已启用 HTTPS。
func (h *Handler) TLSActive() bool {
	return h.tls.Active()
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
	mux.HandleFunc("/tunnels/stats", h.handleTunnelStats)
	mux.HandleFunc("/node/status", h.handleNodeStatus)
	mux.HandleFunc("/logs", h.handleLogs)
	mux.HandleFunc("/logs/stream", h.handleLogStream) // WebSocket
	mux.HandleFunc("/port/check", h.handlePortCheck)
	mux.HandleFunc("/cert/generate", h.handleCertGenerate)
	mux.HandleFunc("/cert/inspect", h.handleCertInspect)
	mux.HandleFunc("/hopchain/parse", h.handleHopChainParse)
	mux.HandleFunc("/tunnel/test", h.handleTunnelTest)
	mux.HandleFunc("/proxy/test", h.handleProxyTest)
	mux.HandleFunc("/version", h.handleVersion)
	mux.HandleFunc("/version/check", h.handleVersionCheck)
	mux.HandleFunc("/auth/settings", h.handleAuthSettings)
	mux.HandleFunc("/web/tls", h.handleWebTLS)
	mux.HandleFunc("/web/tls/token", h.handleWebTLSToken)
	mux.HandleFunc("/geoip/status", h.handleGeoIPStatus)
	mux.HandleFunc("/geoip/download", h.handleGeoIPDownload)
	mux.HandleFunc("/geoip/settings", h.handleGeoIPSettings)
	mux.HandleFunc("/geoip/cities", h.handleGeoIPCities)

	return mux
}

// handleVersion 返回当前二进制的版本信息。
func (h *Handler) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "方法不允许")
		return
	}
	writeJSON(w, 0, "success", map[string]interface{}{
		"version":         h.version,
		"commit":          h.commit,
		"variant":         h.variant,
		"repo":            h.repo,
		"geoip_available": proxy.GeoIPAvailable(),
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

// handleGeoIPStatus 返回当前 GeoIP 能力状态
func (h *Handler) handleGeoIPStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "方法不允许")
		return
	}
	writeJSON(w, 0, "success", proxy.CurrentGeoIPStatus())
}

// handleGeoIPDownload 下载 / 更新城市级数据库（Merged-IP）
func (h *Handler) handleGeoIPDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	h.log.Info("开始下载 GeoIP 城市数据库（Merged-IP）...")
	src, size, err := proxy.DownloadCityDatabase(req.URL)
	if err != nil {
		h.log.Errorf("GeoIP 城市数据库下载失败: %v", err)
		writeError(w, 50002, "下载失败: "+err.Error())
		return
	}
	h.log.Infof("GeoIP 城市数据库下载完成: %s (%.1f MB)", src, float64(size)/1024/1024)
	writeJSON(w, 0, "success", proxy.CurrentGeoIPStatus())
}

// handleGeoIPCities 返回指定国家的省/城市下拉选项（首次调用会在后台构建索引）
func (h *Handler) handleGeoIPCities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "方法不允许")
		return
	}
	writeJSON(w, 0, "success", proxy.CityCatalog(r.URL.Query().Get("country")))
}

// handleGeoIPSettings 开关城市级数据库（立即热加载，无需重启）
func (h *Handler) handleGeoIPSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeError(w, 405, "方法不允许")
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}

	if req.Enabled {
		path := proxy.CityDatabasePath()
		if path == "" {
			writeError(w, 500, "无法确定配置目录")
			return
		}
		if _, err := os.Stat(path); err != nil {
			writeError(w, 400, "尚未下载城市数据库，请先点击「下载/更新」")
			return
		}
		if err := proxy.LoadGeoIPDatabaseFile(path); err != nil {
			writeError(w, 500, "加载城市数据库失败: "+err.Error())
			return
		}
		h.log.Info("已启用 GeoIP 城市级数据库")
	} else {
		// 回退为内置 Country 库
		if err := proxy.LoadBuiltinGeoDatabase(); err != nil {
			h.log.Warnf("回退内置 GeoIP 库失败: %v", err)
		}
		h.log.Info("已切换回内置 GeoIP Country 数据库")
	}

	if err := proxy.SaveGeoIPSettings(proxy.GeoIPSettings{Enabled: req.Enabled}); err != nil {
		writeError(w, 500, "保存设置失败: "+err.Error())
		return
	}
	writeJSON(w, 0, "success", proxy.CurrentGeoIPStatus())
}

// handleLogin 登录接口
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	if !h.auth.Enabled() {
		writeError(w, 40003, "访问密码未开启，无需登录")
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
	if !h.auth.Verify(req.Username, req.Password) {
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

// handleAuthSettings WEB 访问密码设置：GET 查询，PUT 开启/关闭并设置凭据。
// 开启成功后返回 token，便于当前会话无缝续用（否则下一次请求即需登录）。
func (h *Handler) handleAuthSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 0, "success", map[string]interface{}{
			"enabled":  h.auth.Enabled(),
			"username": h.auth.Username(),
		})
	case http.MethodPut:
		var req struct {
			Enabled  bool   `json:"enabled"`
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "请求格式错误")
			return
		}
		var err error
		if req.Enabled {
			err = h.auth.Set(req.Username, req.Password)
		} else {
			err = h.auth.Disable()
		}
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		resp := map[string]interface{}{
			"enabled":  h.auth.Enabled(),
			"username": h.auth.Username(),
		}
		if req.Enabled {
			h.log.Infof("WEB 访问密码已开启 (user=%s)", req.Username)
			if token, exp, err := generateJWT(req.Username, h.jwtSecret); err == nil {
				resp["token"] = token
				resp["expires_at"] = exp
			}
		} else {
			h.log.Info("WEB 访问密码已关闭")
		}
		writeJSON(w, 0, "success", resp)
	default:
		writeError(w, 405, "方法不允许")
	}
}

// handleWebTLS WEB 面板 HTTPS 设置：GET 查询，PUT 保存并热切换监听。
// 支持直接填写证书/私钥 PEM 内容（写入配置目录 certs/web.crt|web.key），
// 也支持指定已有文件路径。保存后会重启监听，需改用 https:// 重新访问。
func (h *Handler) handleWebTLS(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s := h.tls.Settings()
		resp := map[string]interface{}{
			"active":       h.tls.Active(),
			"deploy_token": h.tls.DeployToken(),
		}
		// 回填已有证书内容，便于在界面上查看/修改
		if s.CertFile != "" {
			if data, err := os.ReadFile(s.CertFile); err == nil {
				resp["cert_pem"] = string(data)
			}
		}
		if s.KeyFile != "" {
			if data, err := os.ReadFile(s.KeyFile); err == nil {
				resp["key_pem"] = string(data)
			}
		}
		writeJSON(w, 0, "success", mergeTLSSettings(resp, s))
	case http.MethodPut:
		var req struct {
			Enabled  bool   `json:"enabled"`
			CertPEM  string `json:"cert_pem"`
			KeyPEM   string `json:"key_pem"`
			CertFile string `json:"cert_file"`
			KeyFile  string `json:"key_file"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, 400, "请求格式错误")
			return
		}
		old := h.tls.Settings()
		next, err := h.resolveTLSSettings(req.Enabled, req.CertPEM, req.KeyPEM, req.CertFile, req.KeyFile, old)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if err := h.tls.Save(next); err != nil {
			writeError(w, 500, "保存设置失败: "+err.Error())
			return
		}

		changed := old.Enabled != next.Enabled || old.CertFile != next.CertFile || old.KeyFile != next.KeyFile
		resp := map[string]interface{}{
			"active":     h.tls.Active(),
			"restarting": changed,
		}
		if next.Enabled {
			h.log.Infof("WEB 面板 HTTPS 已开启（证书 %s）", next.CertFile)
		} else {
			h.log.Info("WEB 面板 HTTPS 已关闭")
		}
		writeJSON(w, 0, "success", mergeTLSSettings(resp, next))

		if changed {
			h.scheduleTLSReload()
		}
	default:
		writeError(w, 405, "方法不允许")
	}
}

// mergeTLSSettings 合并设置字段到响应
func mergeTLSSettings(resp map[string]interface{}, s WebTLSSettings) map[string]interface{} {
	resp["enabled"] = s.Enabled
	resp["cert_file"] = s.CertFile
	resp["key_file"] = s.KeyFile
	return resp
}

// resolveTLSSettings 根据请求解析最终设置：优先使用 PEM 内容，其次文件路径，最后沿用旧值。
func (h *Handler) resolveTLSSettings(enabled bool, certPEM, keyPEM, certFile, keyFile string, old WebTLSSettings) (WebTLSSettings, error) {
	next := WebTLSSettings{Enabled: enabled, CertFile: old.CertFile, KeyFile: old.KeyFile}

	if strings.TrimSpace(certPEM) != "" || strings.TrimSpace(keyPEM) != "" {
		if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(keyPEM) == "" {
			return next, fmt.Errorf("证书与私钥内容必须同时填写")
		}
		if _, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err != nil {
			return next, fmt.Errorf("证书或私钥内容无效: %w", err)
		}
		if err := WritePEMFile(WebCertPath(), []byte(certPEM), 0644); err != nil {
			return next, fmt.Errorf("保存证书失败: %w", err)
		}
		if err := WritePEMFile(WebKeyPath(), []byte(keyPEM), 0600); err != nil {
			return next, fmt.Errorf("保存私钥失败: %w", err)
		}
		next.CertFile = WebCertPath()
		next.KeyFile = WebKeyPath()
	} else {
		if f := strings.TrimSpace(certFile); f != "" {
			next.CertFile = f
		}
		if f := strings.TrimSpace(keyFile); f != "" {
			next.KeyFile = f
		}
	}

	if enabled {
		if next.CertFile == "" || next.KeyFile == "" {
			return next, fmt.Errorf("启用 HTTPS 需要填写证书内容或指定证书文件")
		}
		if _, err := tls.LoadX509KeyPair(next.CertFile, next.KeyFile); err != nil {
			return next, fmt.Errorf("证书加载失败: %w", err)
		}
	}
	return next, nil
}

// scheduleTLSReload 延迟触发监听重启，保证当前响应先返回客户端。
func (h *Handler) scheduleTLSReload() {
	if h.reloadTLS == nil {
		return
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		h.reloadTLS()
	}()
}

// handleWebTLSToken 重新生成 certd 部署令牌
func (h *Handler) handleWebTLSToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	token, err := h.tls.RegenerateDeployToken()
	if err != nil {
		writeError(w, 500, "生成失败: "+err.Error())
		return
	}
	h.log.Info("certd 部署令牌已重置")
	writeJSON(w, 0, "success", map[string]string{"deploy_token": token})
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

// handleTunnelStats 获取隧道的连接与流量统计（含已停止隧道的累计值）
func (h *Handler) handleTunnelStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "方法不允许")
		return
	}
	stats := map[string]tunnel.TunnelStats{}
	if h.tunnelMgr != nil {
		stats = h.tunnelMgr.Stats()
	}
	writeJSON(w, 0, "success", stats)
}

// handleNodeStatus 获取节点级运行状态：概览、活动连接、流量与访问历史
func (h *Handler) handleNodeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "方法不允许")
		return
	}
	limit := 200
	if l := r.URL.Query().Get("history_limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}
	status := tunnel.NodeStatus{
		Connections:   []proxy.ConnSnapshot{},
		AccessHistory: []proxy.AccessRecord{},
		Tunnels:       []tunnel.TunnelTraffic{},
	}
	if h.tunnelMgr != nil && h.cfgMgr != nil {
		status = h.tunnelMgr.NodeStatus(h.cfgMgr.List(), limit)
	}
	status.Version = h.version
	writeJSON(w, 0, "success", status)
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

// handleCertGenerate 生成自签证书（返回 PEM 文本）
func (h *Handler) handleCertGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	certPEM, keyPEM, err := proxy.GenerateSelfSignedPEM()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 0, "success", map[string]string{
		"cert_pem": certPEM,
		"key_pem":  keyPEM,
	})
}

// handleCertInspect 解析证书 PEM，返回使用者公用名与到期时间等摘要信息
func (h *Handler) handleCertInspect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	var req struct {
		CertPEM string `json:"cert_pem"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	info, err := proxy.InspectCertificate(req.CertPEM)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 0, "success", info)
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

// handleProxyTest 测试代理监听端口连通性：经该代理访问目标地址（默认 api.ip.sb/ip）并返回出口 IP。
// 安全约束：只探测本机回环 127.0.0.1 上的代理端口，避免被用作 SSRF 跳板。
func (h *Handler) handleProxyTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	var req struct {
		Protocol string `json:"protocol"`
		Port     int    `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		Target   string `json:"target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	if req.Port < 1 || req.Port > 65535 {
		writeError(w, 400, "无效的代理端口")
		return
	}

	res, err := proxy.Probe(proxy.ProbeOptions{
		Protocol: req.Protocol,
		Host:     "127.0.0.1",
		Port:     req.Port,
		Username: req.Username,
		Password: req.Password,
		Target:   req.Target,
	})
	if err != nil {
		h.log.Warnf("代理连通性测试失败 (port=%d protocol=%s): %v", req.Port, req.Protocol, err)
		writeError(w, 50002, "代理连通性测试失败: "+err.Error())
		return
	}
	h.log.Infof("代理连通性测试成功 (port=%d protocol=%s): 出口 IP=%s 耗时 %dms", req.Port, req.Protocol, res.IP, res.ElapsedMS)
	writeJSON(w, 0, "success", res)
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
