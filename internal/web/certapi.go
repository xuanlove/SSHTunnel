package web

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"sshtunnel/internal/config"
)

// 证书部署目标类型
const (
	CertTargetWeb    = "web"    // WEB 面板自身
	CertTargetTunnel = "tunnel" // HTTPS 代理监听器
)

// CertTargetInfo 证书部署目标描述（供 certd 等外部工具发现）
type CertTargetInfo struct {
	Target     string `json:"target"`
	Label      string `json:"label"`
	TunnelID   string `json:"tunnel_id,omitempty"`
	TunnelName string `json:"tunnel_name,omitempty"`
	ListenerID string `json:"listener_id,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	// CertConfigured 是否已配置证书：
	// WEB 面板表示证书文件存在；隧道监听器表示配置中已保存 cert_pem/key_pem 文本
	CertConfigured bool `json:"cert_configured"`
	Enabled        bool `json:"enabled"`
	Running        bool `json:"running"`
}

// CertTargets 列出全部可部署证书的目标（WEB 面板 + 所有 HTTPS 代理监听器）
func (h *Handler) CertTargets() []CertTargetInfo {
	out := make([]CertTargetInfo, 0, 4)

	web := h.tls.Settings()
	out = append(out, CertTargetInfo{
		Target:         CertTargetWeb,
		Label:          "WEB 面板",
		CertConfigured: fileExistsAt(web.CertFile) && fileExistsAt(web.KeyFile),
		Enabled:        web.Enabled,
		Running:        h.tls.Active(),
	})

	for _, cfg := range h.cfgMgr.List() {
		for _, pl := range cfg.ProxyListeners {
			if pl.Protocol != config.ProxyHTTPS {
				continue
			}
			configured := pl.TLS != nil &&
				strings.TrimSpace(pl.TLS.CertPEM) != "" &&
				strings.TrimSpace(pl.TLS.KeyPEM) != ""
			running := h.tunnelMgr != nil && h.tunnelMgr.IsRunning(cfg.ID)
			out = append(out, CertTargetInfo{
				Target:         CertTargetTunnel,
				Label:          fmt.Sprintf("%s / %s :%d", cfg.Name, pl.Protocol, pl.ListenPort),
				TunnelID:       cfg.ID,
				TunnelName:     cfg.Name,
				ListenerID:     pl.ID,
				Protocol:       string(pl.Protocol),
				CertConfigured: configured,
				Enabled:        true,
				Running:        running,
			})
		}
	}
	return out
}

func fileExistsAt(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// certdTokenMiddleware certd 部署 API 的令牌校验（独立于 WEB 登录鉴权）。
func certdTokenMiddleware(token func() string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			want := token()
			got := strings.TrimSpace(r.Header.Get("X-API-Token"))
			if got == "" {
				got = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				got = strings.TrimSpace(got)
			}
			if want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				writeError(w, 401, "未授权：部署 Token 无效")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// certRouter certd 部署相关路由（路径不含 /api 前缀）
func (h *Handler) certRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/cert/targets", h.handleCertTargets)
	mux.HandleFunc("/cert/read", h.handleCertRead)
	mux.HandleFunc("/cert/deploy", h.handleCertDeploy)
	return mux
}

// handleCertTargets GET /api/cert/targets 列出可部署目标
func (h *Handler) handleCertTargets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "方法不允许")
		return
	}
	writeJSON(w, 0, "success", map[string]interface{}{"targets": h.CertTargets()})
}

// handleCertRead GET /api/cert/read?target=web
//
//	GET /api/cert/read?target=tunnel&tunnel_id=xxx&listener_id=yyy
func (h *Handler) handleCertRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "方法不允许")
		return
	}
	target, certPEM, keyPEM, err := h.resolveTargetPEM(r.URL.Query().Get("target"),
		r.URL.Query().Get("tunnel_id"), r.URL.Query().Get("listener_id"))
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	writeJSON(w, 0, "success", map[string]interface{}{
		"target":   target,
		"cert_pem": certPEM,
		"key_pem":  keyPEM,
	})
}

// certDeployRequest 证书部署请求（兼容 certd 自定义脚本 / HTTP 上传）
type certDeployRequest struct {
	Target     string `json:"target"` // web | tunnel
	TunnelID   string `json:"tunnel_id"`
	ListenerID string `json:"listener_id"`
	CertPEM    string `json:"cert_pem"`
	KeyPEM     string `json:"key_pem"`
	// Restart 部署后是否重启监听/隧道（默认 true）
	Restart *bool `json:"restart"`
	// Enable 是否同时启用 WEB 面板 HTTPS（仅 target=web，默认 true）
	Enable *bool `json:"enable"`
}

// handleCertDeploy POST /api/cert/deploy 部署证书内容并重启生效
func (h *Handler) handleCertDeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "方法不允许")
		return
	}
	var req certDeployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "请求格式错误")
		return
	}
	req.CertPEM = strings.TrimSpace(req.CertPEM)
	req.KeyPEM = strings.TrimSpace(req.KeyPEM)
	if req.CertPEM == "" || req.KeyPEM == "" {
		writeError(w, 400, "缺少证书或私钥内容（cert_pem / key_pem）")
		return
	}
	// 先校验证书与私钥是否匹配，避免写入不可用证书导致服务重启失败
	if _, err := tls.X509KeyPair([]byte(req.CertPEM), []byte(req.KeyPEM)); err != nil {
		writeError(w, 400, "证书或私钥内容无效: "+err.Error())
		return
	}

	restart := true
	if req.Restart != nil {
		restart = *req.Restart
	}

	switch strings.ToLower(strings.TrimSpace(req.Target)) {
	case "", CertTargetWeb:
		enable := true
		if req.Enable != nil {
			enable = *req.Enable
		}
		resp, err := h.deployToWeb(req.CertPEM, req.KeyPEM, enable, restart)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 0, "success", resp)
	case CertTargetTunnel:
		resp, err := h.deployToTunnel(req.CertPEM, req.KeyPEM, req.TunnelID, req.ListenerID, restart)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, 0, "success", resp)
	default:
		writeError(w, 400, "未知的部署目标: "+req.Target)
	}
}

// deployToWeb 写入 WEB 面板证书文件并按需启用、重启监听
func (h *Handler) deployToWeb(certPEM, keyPEM string, enable, restart bool) (map[string]interface{}, error) {
	certFile, keyFile := WebCertPath(), WebKeyPath()
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("无法确定证书存放目录")
	}
	if err := WritePEMFile(certFile, []byte(certPEM), 0644); err != nil {
		return nil, fmt.Errorf("保存证书失败: %w", err)
	}
	if err := WritePEMFile(keyFile, []byte(keyPEM), 0600); err != nil {
		return nil, fmt.Errorf("保存私钥失败: %w", err)
	}
	if err := h.tls.Save(WebTLSSettings{Enabled: enable, CertFile: certFile, KeyFile: keyFile}); err != nil {
		return nil, fmt.Errorf("保存设置失败: %w", err)
	}
	h.log.Infof("certd 已部署 WEB 面板证书: %s", certFile)
	if restart {
		h.scheduleTLSReload()
	}
	return map[string]interface{}{
		"target":    CertTargetWeb,
		"cert_file": certFile,
		"key_file":  keyFile,
		"enabled":   enable,
		"restarted": restart,
	}, nil
}

// deployToTunnel 将证书 PEM 写入监听器配置（文本模式）并按需重启隧道
func (h *Handler) deployToTunnel(certPEM, keyPEM, tunnelID, listenerID string, restart bool) (map[string]interface{}, error) {
	if h.cfgMgr == nil {
		return nil, fmt.Errorf("配置管理不可用")
	}
	cfg, ok := h.cfgMgr.Get(tunnelID)
	if !ok {
		return nil, fmt.Errorf("隧道不存在: %s", tunnelID)
	}
	idx := -1
	for i := range cfg.ProxyListeners {
		if cfg.ProxyListeners[i].ID == listenerID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("监听器不存在: %s", listenerID)
	}
	if cfg.ProxyListeners[idx].Protocol != config.ProxyHTTPS {
		return nil, fmt.Errorf("监听器 %s 不是 HTTPS 协议，无法部署 TLS 证书", listenerID)
	}

	// 复制一份用于持久化，避免修改 Manager 内部状态后才失败
	updated := *cfg
	updated.ProxyListeners = append([]config.ProxyListener(nil), cfg.ProxyListeners...)
	updated.ProxyListeners[idx].TLS = &config.TLSConfig{CertPEM: certPEM, KeyPEM: keyPEM}
	if err := h.cfgMgr.Save(&updated); err != nil {
		return nil, fmt.Errorf("保存配置失败: %w", err)
	}
	h.log.Infof("certd 已部署证书到隧道 %s 的 HTTPS 监听器 %s（文本模式）", cfg.Name, listenerID)

	running := h.tunnelMgr != nil && h.tunnelMgr.IsRunning(cfg.ID)
	restarted := false
	if restart && running {
		if err := h.tunnelMgr.Stop(cfg.ID); err != nil {
			return nil, fmt.Errorf("停止隧道失败: %w", err)
		}
		if err := h.tunnelMgr.Start(&updated); err != nil {
			h.cfgMgr.SetStatus(cfg.ID, config.StatusError)
			return nil, fmt.Errorf("重启隧道失败: %w", err)
		}
		h.cfgMgr.SetStatus(cfg.ID, config.StatusRunning)
		restarted = true
		h.log.Infof("隧道已重启以加载新证书: %s", cfg.Name)
	}
	return map[string]interface{}{
		"target":          CertTargetTunnel,
		"tunnel_id":       cfg.ID,
		"tunnel_name":     cfg.Name,
		"listener_id":     listenerID,
		"cert_configured": true,
		"running":         running,
		"restarted":       restarted,
	}, nil
}

// resolveTargetPEM 解析部署目标对应的证书/私钥 PEM 文本
func (h *Handler) resolveTargetPEM(target, tunnelID, listenerID string) (string, string, string, error) {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case CertTargetWeb:
		s := h.tls.Settings()
		if s.CertFile == "" || s.KeyFile == "" {
			return "", "", "", fmt.Errorf("WEB 面板尚未配置证书")
		}
		certPEM, err := os.ReadFile(s.CertFile)
		if err != nil {
			return "", "", "", fmt.Errorf("证书文件不存在: %w", err)
		}
		keyPEM, err := os.ReadFile(s.KeyFile)
		if err != nil {
			return "", "", "", fmt.Errorf("私钥文件不存在: %w", err)
		}
		return CertTargetWeb, string(certPEM), string(keyPEM), nil
	case CertTargetTunnel:
		if h.cfgMgr == nil {
			return "", "", "", fmt.Errorf("配置管理不可用")
		}
		cfg, ok := h.cfgMgr.Get(tunnelID)
		if !ok {
			return "", "", "", fmt.Errorf("隧道不存在: %s", tunnelID)
		}
		for _, pl := range cfg.ProxyListeners {
			if pl.ID != listenerID {
				continue
			}
			if pl.Protocol != config.ProxyHTTPS || pl.TLS == nil ||
				strings.TrimSpace(pl.TLS.CertPEM) == "" || strings.TrimSpace(pl.TLS.KeyPEM) == "" {
				return "", "", "", fmt.Errorf("监听器未配置 HTTPS 证书")
			}
			return CertTargetTunnel, pl.TLS.CertPEM, pl.TLS.KeyPEM, nil
		}
		return "", "", "", fmt.Errorf("监听器不存在: %s", listenerID)
	default:
		return "", "", "", fmt.Errorf("未知的部署目标: %s", target)
	}
}
