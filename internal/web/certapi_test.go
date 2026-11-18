package web

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
	"sshtunnel/internal/tunnel"
)

// generateTestCertPEM 生成自签证书与私钥 PEM（用于部署测试）
func generateTestCertPEM(t *testing.T) (certPEM, keyPEM string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("生成序列号失败: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "test.local"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"test.local"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("签发证书失败: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("序列化私钥失败: %v", err)
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM
}

func newCertTestHandler(t *testing.T) *Handler {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := NewHandler(nil, nil, logger.NewTest(), []byte("secret"), "", "", nil)
	h.tls = NewTLSStore(filepath.Join(t.TempDir(), "web_tls.json"))
	h.tls.EnsureDeployToken()
	return h
}

// TestTLSStorePersistence 设置与部署令牌持久化
func TestTLSStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web_tls.json")
	st := NewTLSStore(path)
	token := st.EnsureDeployToken()
	if token == "" {
		t.Fatalf("应自动生成部署令牌")
	}
	if err := st.Save(WebTLSSettings{Enabled: true, CertFile: "/tmp/a.crt", KeyFile: "/tmp/a.key"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	reloaded := NewTLSStore(path)
	if got := reloaded.Settings(); !got.Enabled || got.CertFile != "/tmp/a.crt" || got.KeyFile != "/tmp/a.key" {
		t.Errorf("重新加载设置不符: %+v", got)
	}
	if got := reloaded.DeployToken(); got != token {
		t.Errorf("令牌应保留: got %q want %q", got, token)
	}
	if c, k, ok := reloaded.Effective(); !ok || c != "/tmp/a.crt" || k != "/tmp/a.key" {
		t.Errorf("Effective 应返回启用状态的文件路径")
	}

	// 关闭后 Effective 应失效
	if err := reloaded.Save(WebTLSSettings{Enabled: false, CertFile: "/tmp/a.crt", KeyFile: "/tmp/a.key"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, _, ok := reloaded.Effective(); ok {
		t.Errorf("关闭后不应生效")
	}

	// 令牌重置
	newToken, err := reloaded.RegenerateDeployToken()
	if err != nil || newToken == "" || newToken == token {
		t.Errorf("重置令牌失败: %q %v", newToken, err)
	}
}

// TestCertdTokenMiddleware 部署 Token 鉴权
func TestCertdTokenMiddleware(t *testing.T) {
	h := newCertTestHandler(t)
	token := h.tls.DeployToken()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := certdTokenMiddleware(h.tls.DeployToken)(next)

	cases := []struct {
		name   string
		header string
		value  string
		want   int
	}{
		{"缺少令牌", "", "", http.StatusUnauthorized},
		{"错误令牌", "X-API-Token", "bad", http.StatusUnauthorized},
		{"正确令牌", "X-API-Token", token, http.StatusOK},
		{"Bearer 令牌", "Authorization", "Bearer " + token, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/cert/targets", nil)
			if c.header != "" {
				req.Header.Set(c.header, c.value)
			}
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d", rec.Code, c.want)
			}
		})
	}
}

// TestWebTLSContentSave 通过系统设置接口填写证书内容并启用
func TestWebTLSContentSave(t *testing.T) {
	h := newCertTestHandler(t)
	certPEM, keyPEM := generateTestCertPEM(t)

	body, _ := json.Marshal(map[string]interface{}{
		"enabled":  true,
		"cert_pem": certPEM,
		"key_pem":  keyPEM,
	})
	req := httptest.NewRequest("PUT", "/web/tls", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.handleWebTLS(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("保存失败: %d %s", rec.Code, rec.Body.String())
	}

	s := h.tls.Settings()
	if !s.Enabled || s.CertFile != WebCertPath() || s.KeyFile != WebKeyPath() {
		t.Errorf("设置不符: %+v", s)
	}
	for _, p := range []string{WebCertPath(), WebKeyPath()} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("证书文件未写入 %s: %v", p, err)
		}
	}

	// GET 应回填证书内容
	rec2 := httptest.NewRecorder()
	h.handleWebTLS(rec2, httptest.NewRequest("GET", "/web/tls", nil))
	var resp struct {
		Data struct {
			Enabled   bool   `json:"enabled"`
			CertPEM   string `json:"cert_pem"`
			KeyPEM    string `json:"key_pem"`
			DeployTok string `json:"deploy_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !resp.Data.Enabled || !strings.Contains(resp.Data.CertPEM, "BEGIN CERTIFICATE") {
		t.Errorf("GET 应回填证书内容")
	}
	if resp.Data.KeyPEM == "" || resp.Data.DeployTok == "" {
		t.Errorf("GET 应返回私钥与部署令牌")
	}

	// 无效证书内容应被拒绝
	badBody, _ := json.Marshal(map[string]interface{}{"enabled": true, "cert_pem": "bad", "key_pem": "bad"})
	rec3 := httptest.NewRecorder()
	h.handleWebTLS(rec3, httptest.NewRequest("PUT", "/web/tls", bytes.NewReader(badBody)))
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("无效证书应返回 400，实际 %d", rec3.Code)
	}
}

// TestCertDeployWeb certd 部署证书到 WEB 面板
func TestCertDeployWeb(t *testing.T) {
	h := newCertTestHandler(t)
	certPEM, keyPEM := generateTestCertPEM(t)

	body, _ := json.Marshal(map[string]interface{}{
		"target":   "web",
		"cert_pem": certPEM,
		"key_pem":  keyPEM,
	})
	req := httptest.NewRequest("POST", "/cert/deploy", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.handleCertDeploy(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("部署失败: %d %s", rec.Code, rec.Body.String())
	}
	s := h.tls.Settings()
	if !s.Enabled {
		t.Errorf("部署后应启用 HTTPS")
	}
	if _, err := os.Stat(WebCertPath()); err != nil {
		t.Errorf("证书文件未写入: %v", err)
	}

	// 缺少内容应报错
	bad, _ := json.Marshal(map[string]interface{}{"target": "web"})
	rec2 := httptest.NewRecorder()
	h.handleCertDeploy(rec2, httptest.NewRequest("POST", "/cert/deploy", bytes.NewReader(bad)))
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("缺少证书内容应返回 400")
	}
}

// TestCertDeployTunnel certd 部署证书到 HTTPS 监听器（不重启）
func TestCertDeployTunnel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfgMgr, err := config.NewManager()
	if err != nil {
		t.Fatalf("创建配置管理失败: %v", err)
	}
	h := NewHandler(cfgMgr, tunnel.NewManager(logger.NewTest()), logger.NewTest(), []byte("secret"), "", "", nil)

	cfg := config.TunnelConfig{
		Name:       "证书测试",
		HopChain:   []config.HopConfig{{User: "u", Host: "127.0.0.1", Port: 22, AuthType: "password"}},
		TunnelType: config.TunnelProxy,
		ProxyListeners: []config.ProxyListener{
			{ID: "https-1", Protocol: config.ProxyHTTPS, ListenPort: 18443},
		},
	}
	if err := cfgMgr.Save(&cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	certPEM, keyPEM := generateTestCertPEM(t)
	body, _ := json.Marshal(map[string]interface{}{
		"target":      "tunnel",
		"tunnel_id":   cfg.ID,
		"listener_id": "https-1",
		"cert_pem":    certPEM,
		"key_pem":     keyPEM,
		"restart":     false,
	})
	rec := httptest.NewRecorder()
	h.handleCertDeploy(rec, httptest.NewRequest("POST", "/cert/deploy", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("部署失败: %d %s", rec.Code, rec.Body.String())
	}

	got, ok := cfgMgr.Get(cfg.ID)
	if !ok || got.ProxyListeners[0].TLS == nil {
		t.Fatalf("配置未更新 TLS 信息")
	}
	tlsCfg := got.ProxyListeners[0].TLS
	if !strings.Contains(tlsCfg.CertPEM, "BEGIN CERTIFICATE") {
		t.Errorf("监听器证书应保存为 PEM 文本")
	}
	if !strings.Contains(tlsCfg.KeyPEM, "PRIVATE KEY") {
		t.Errorf("监听器私钥应保存为 PEM 文本")
	}
	if tlsCfg.CertFile != "" || tlsCfg.KeyFile != "" {
		t.Errorf("文本模式下不应再记录文件路径: %q %q", tlsCfg.CertFile, tlsCfg.KeyFile)
	}

	// 读取证书内容
	readReq := httptest.NewRequest("GET", "/cert/read?target=tunnel&tunnel_id="+cfg.ID+"&listener_id=https-1", nil)
	rec2 := httptest.NewRecorder()
	h.handleCertRead(rec2, readReq)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), "BEGIN CERTIFICATE") {
		t.Errorf("读取证书失败: %d", rec2.Code)
	}

	// 非 HTTPS 监听器应拒绝
	cfg2 := config.TunnelConfig{
		Name: "socks 隧道",
		ProxyListeners: []config.ProxyListener{
			{ID: "s5", Protocol: config.ProxySOCKS5, ListenPort: 18444},
		},
	}
	if err := cfgMgr.Save(&cfg2); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	body2, _ := json.Marshal(map[string]interface{}{
		"target": "tunnel", "tunnel_id": cfg2.ID, "listener_id": "s5",
		"cert_pem": certPEM, "key_pem": keyPEM,
	})
	rec3 := httptest.NewRecorder()
	h.handleCertDeploy(rec3, httptest.NewRequest("POST", "/cert/deploy", bytes.NewReader(body2)))
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("非 HTTPS 监听器应返回 400，实际 %d", rec3.Code)
	}
}

// TestCertInspectEndpoint 证书摘要接口：返回使用者 CN 与到期时间
func TestCertInspectEndpoint(t *testing.T) {
	h := newCertTestHandler(t)
	certPEM, _ := generateTestCertPEM(t)

	body, _ := json.Marshal(map[string]string{"cert_pem": certPEM})
	rec := httptest.NewRecorder()
	h.handleCertInspect(rec, httptest.NewRequest("POST", "/cert/inspect", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("解析失败: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			SubjectCN string `json:"subject_cn"`
			NotAfter  string `json:"not_after"`
			Expired   bool   `json:"expired"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.SubjectCN != "test.local" {
		t.Errorf("使用者 CN = %q, want test.local", resp.Data.SubjectCN)
	}
	if resp.Data.NotAfter == "" || resp.Data.Expired {
		t.Errorf("到期信息异常: %+v", resp.Data)
	}

	// 非法证书 → 400
	bad, _ := json.Marshal(map[string]string{"cert_pem": "bad"})
	rec2 := httptest.NewRecorder()
	h.handleCertInspect(rec2, httptest.NewRequest("POST", "/cert/inspect", bytes.NewReader(bad)))
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("非法证书应返回 400，实际 %d", rec2.Code)
	}

	// 非 POST → 405
	rec3 := httptest.NewRecorder()
	h.handleCertInspect(rec3, httptest.NewRequest("GET", "/cert/inspect", nil))
	if rec3.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET 应返回 405，实际 %d", rec3.Code)
	}
}

// TestCertToolRoutesBypassCertdToken 面板内部的证书工具接口（生成/解析）走登录鉴权，
// 不应被 certd 的 /api/cert/ Token 中间件拦截；部署接口仍必须携带 Token。
func TestCertToolRoutesBypassCertdToken(t *testing.T) {
	h := newCertTestHandler(t)
	mux := NewServer(Config{}, h).buildRoutes()
	certPEM, _ := generateTestCertPEM(t)
	body, _ := json.Marshal(map[string]string{"cert_pem": certPEM})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/cert/inspect", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/cert/inspect 应放行，实际 %d: %s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	mux.ServeHTTP(rec2, httptest.NewRequest("POST", "/api/cert/generate", nil))
	if rec2.Code != http.StatusOK {
		t.Errorf("/api/cert/generate 应放行，实际 %d: %s", rec2.Code, rec2.Body.String())
	}

	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, httptest.NewRequest("GET", "/api/cert/targets", nil))
	if rec3.Code != http.StatusUnauthorized {
		t.Errorf("/api/cert/targets 应要求部署 Token，实际 %d", rec3.Code)
	}
}

// TestCertTargets 目标列表包含 WEB 面板与 HTTPS 监听器
func TestCertTargets(t *testing.T) {
	h := newCertTestHandler(t)
	cfgMgr, err := config.NewManager()
	if err != nil {
		t.Fatalf("创建配置管理失败: %v", err)
	}
	h.cfgMgr = cfgMgr
	cfg := config.TunnelConfig{
		Name: "列表测试",
		ProxyListeners: []config.ProxyListener{
			{ID: "h1", Protocol: config.ProxyHTTPS, ListenPort: 18445},
			{ID: "s1", Protocol: config.ProxySOCKS5, ListenPort: 18446},
		},
	}
	if err := cfgMgr.Save(&cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	targets := h.CertTargets()
	if len(targets) != 2 {
		t.Fatalf("应返回 WEB 面板 + 1 个 HTTPS 监听器，实际 %d", len(targets))
	}
	if targets[0].Target != CertTargetWeb {
		t.Errorf("第一项应为 WEB 面板")
	}
	if targets[1].Target != CertTargetTunnel || targets[1].ListenerID != "h1" {
		t.Errorf("第二项应为 HTTPS 监听器: %+v", targets[1])
	}
}
