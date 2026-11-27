package web

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/ipcert"
	"sshtunnel/internal/logger"
)

// 测试隧道流量统计接口（无运行中隧道时返回空对象）
func TestTunnelStatsEmpty(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	w := httptest.NewRecorder()
	h.handleTunnelStats(w, httptest.NewRequest("GET", "/tunnels/stats", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var resp struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Errorf("期望空统计，实际 %v", resp.Data)
	}

	// 非 GET 方法应拒绝
	w2 := httptest.NewRecorder()
	h.handleTunnelStats(w2, httptest.NewRequest("POST", "/tunnels/stats", strings.NewReader("")))
	if w2.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 期望 405，实际 %d", w2.Code)
	}
}

// 测试节点状态接口（无运行实例时返回空结构）
func TestNodeStatusEmpty(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil).
		WithVersion("v-test", "abc", "web", "")
	w := httptest.NewRecorder()
	h.handleNodeStatus(w, httptest.NewRequest("GET", "/node/status?history_limit=10", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var resp struct {
		Data struct {
			Version       string        `json:"version"`
			TunnelTotal   int           `json:"tunnel_total"`
			Connections   []interface{} `json:"connections"`
			AccessHistory []interface{} `json:"access_history"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.Version != "v-test" || resp.Data.TunnelTotal != 0 {
		t.Errorf("节点状态内容不符: %+v", resp.Data)
	}
	if resp.Data.Connections == nil || resp.Data.AccessHistory == nil {
		t.Errorf("连接与访问历史应为空数组而非 null: %+v", resp.Data)
	}

	// 非 GET 方法应拒绝
	w2 := httptest.NewRecorder()
	h.handleNodeStatus(w2, httptest.NewRequest("POST", "/node/status", strings.NewReader("")))
	if w2.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 期望 405，实际 %d", w2.Code)
	}
}

// 测试 WEB TLS 设置接口：显式指定的证书/私钥文件必须是有效 PEM，
// 防止任意路径被持久化后经 GET 回读文件内容（任意文件读取）
func TestWebTLSSettingsRejectsNonPEMFiles(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := generateTestCertPEM(t)
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, []byte(certPEM), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte(keyPEM), 0600); err != nil {
		t.Fatal(err)
	}
	// 非 PEM 的普通文件（模拟 /etc/shadow 等任意系统文件）
	badFile := filepath.Join(dir, "not-pem.txt")
	if err := os.WriteFile(badFile, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0600); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	h.tls = NewTLSStore(filepath.Join(dir, "web_tls.json"))

	put := func(certF, keyF string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"enabled":false,"cert_file":%q,"key_file":%q}`, certF, keyF)
		w := httptest.NewRecorder()
		h.handleWebTLS(w, httptest.NewRequest(http.MethodPut, "/api/web/tls", strings.NewReader(body)))
		return w
	}

	// 未启用 HTTPS 时提交非 PEM / 不存在的路径也应被拒绝（即使关闭也不放行任意路径）
	if w := put(badFile, keyFile); w.Code != http.StatusBadRequest {
		t.Errorf("非 PEM 证书文件应被拒绝，实际 %d: %s", w.Code, w.Body.String())
	}
	if w := put(certFile, badFile); w.Code != http.StatusBadRequest {
		t.Errorf("非 PEM 私钥文件应被拒绝，实际 %d: %s", w.Code, w.Body.String())
	}
	if w := put(filepath.Join(dir, "missing.pem"), keyFile); w.Code != http.StatusBadRequest {
		t.Errorf("不存在的证书路径应被拒绝，实际 %d: %s", w.Code, w.Body.String())
	}

	// 拒绝后设置不得被写入（路径未持久化，GET 不回读）
	w := httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodGet, "/api/web/tls", nil))
	var resp struct {
		Data struct {
			CertFile string `json:"cert_file"`
			KeyFile  string `json:"key_file"`
			CertPEM  string `json:"cert_pem"`
			KeyPEM   string `json:"key_pem"`
			HasKey   bool   `json:"has_key"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Data.CertFile != "" || resp.Data.KeyFile != "" {
		t.Errorf("被拒绝的路径不应持久化，实际 cert_file=%q key_file=%q", resp.Data.CertFile, resp.Data.KeyFile)
	}

	// 有效 PEM 对即使未启用也应保存成功，且 GET 可回读
	if w := put(certFile, keyFile); w.Code != http.StatusOK {
		t.Fatalf("有效 PEM 文件应保存成功，实际 %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodGet, "/api/web/tls", nil))
	resp = struct {
		Data struct {
			CertFile string `json:"cert_file"`
			KeyFile  string `json:"key_file"`
			CertPEM  string `json:"cert_pem"`
			KeyPEM   string `json:"key_pem"`
			HasKey   bool   `json:"has_key"`
		} `json:"data"`
	}{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Data.CertPEM == "" || !strings.Contains(resp.Data.CertPEM, "BEGIN CERTIFICATE") {
		t.Errorf("GET 应回读证书 PEM 内容")
	}
	if !resp.Data.HasKey {
		t.Errorf("GET 应回报 has_key=true")
	}
	// 默认 GET 不返回私钥明文（脱敏断言见 TestWebTLSKeyMaskingAndReveal）
	if resp.Data.KeyPEM != "" {
		t.Errorf("默认 GET 不应返回 key_pem 明文，实际长度 %d", len(resp.Data.KeyPEM))
	}
}

// IP 证书设置接口：GET 返回设置与状态；PUT 校验 IP 合法性
func TestIPCertSettingsAPI(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	m := ipcert.NewManager(t.TempDir(), nil)
	h.SetIPCert(m)

	// GET：默认关闭
	w := httptest.NewRecorder()
	h.handleIPCert(w, httptest.NewRequest(http.MethodGet, "/api/settings/ipcert", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET 应返回 200, got %d", w.Code)
	}
	var got struct {
		Data struct {
			Settings ipcert.Settings `json:"settings"`
			Status   ipcert.Status   `json:"status"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if got.Data.Settings.Enabled {
		t.Errorf("默认应为未启用")
	}

	// PUT：私网 IP 应被拒绝
	body := `{"enabled":true,"ip":"10.0.0.1","http_port":80}`
	w = httptest.NewRecorder()
	h.handleIPCert(w, httptest.NewRequest(http.MethodPut, "/api/settings/ipcert", strings.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Errorf("私网 IP 应返回 400, got %d: %s", w.Code, w.Body.String())
	}

	// PUT：公网 IP + 关闭状态应保存成功
	body = `{"enabled":false,"ip":"203.0.113.10","http_port":8080,"email":"a@example.com"}`
	w = httptest.NewRecorder()
	h.handleIPCert(w, httptest.NewRequest(http.MethodPut, "/api/settings/ipcert", strings.NewReader(body)))
	if w.Code != http.StatusOK {
		t.Fatalf("保存合法设置应返回 200, got %d: %s", w.Code, w.Body.String())
	}
	s := m.Settings()
	if s.IP != "203.0.113.10" || s.Port() != 8080 || s.Email != "a@example.com" {
		t.Errorf("设置未正确保存: %+v", s)
	}
}

// 测试鉴权状态接口（无密码模式）
func TestAuthStatusDisabled(t *testing.T) {
	srv := NewServer(Config{}, NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil))
	req := httptest.NewRequest("GET", "/api/auth/status", nil)
	w := httptest.NewRecorder()
	srv.handleAuthStatus(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	data := resp["data"].(map[string]interface{})
	if data["auth_enabled"] != false {
		t.Errorf("期望 auth_enabled=false，实际 %v", data["auth_enabled"])
	}
}

// 测试无密码模式直接放行
func TestNoAuthMiddlewarePassThrough(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	mw := authMiddleware(func() (bool, []byte, context.Context) { return false, nil, context.Background() })
	mw(next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if !called {
		t.Error("无密码模式应该直接放行")
	}
}

// 测试密码模式缺少 Token 被拒绝
func TestAuthMiddlewareRejectNoToken(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("不应调用下一处理器")
	})
	mw := authMiddleware(func() (bool, []byte, context.Context) { return true, []byte("secret"), context.Background() })
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("期望 401，实际 %d", w.Code)
	}
}

// 测试密码模式携带有效 Token 放行
func TestAuthMiddlewareValidToken(t *testing.T) {
	secret := []byte("test-secret")
	token, _, err := generateJWT("admin", secret)
	if err != nil {
		t.Fatalf("生成 Token 失败: %v", err)
	}
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	mw := authMiddleware(func() (bool, []byte, context.Context) { return true, secret, context.Background() })
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	mw(next).ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Error("有效 Token 应放行")
	}
}

// 测试密码模式 Token 无效被拒绝
func TestAuthMiddlewareInvalidToken(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("不应调用下一处理器")
	})
	mw := authMiddleware(func() (bool, []byte, context.Context) { return true, []byte("secret"), context.Background() })
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	w := httptest.NewRecorder()
	mw(next).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("期望 401，实际 %d", w.Code)
	}
}

// 测试 Token 提取（Header 与 query 参数）
func TestExtractToken(t *testing.T) {
	// Header
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer abc123")
	if got := extractToken(req); got != "abc123" {
		t.Errorf("Header 提取失败: got %s", got)
	}
	// Query 参数
	req = httptest.NewRequest("GET", "/?token=xyz789", nil)
	if got := extractToken(req); got != "xyz789" {
		t.Errorf("Query 提取失败: got %s", got)
	}
	// 无 Token
	req = httptest.NewRequest("GET", "/", nil)
	if got := extractToken(req); got != "" {
		t.Errorf("无 Token 应返回空，实际 %s", got)
	}
}

// 测试 splitPath 路径拆分
func TestSplitPath(t *testing.T) {
	cases := []struct {
		input    string
		expected int
	}{
		{"configs", 1},
		{"configs/abc", 2},
		{"configs/abc/start", 3},
		{"/configs/abc/", 2},
	}
	for _, c := range cases {
		got := splitPath(c.input)
		if len(got) != c.expected {
			t.Errorf("splitPath(%s) = %v, 期望长度 %d", c.input, got, c.expected)
		}
	}
}

// 测试登录接口
func TestLogin(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), []byte("secret"), "admin", "pass123", nil)

	// 正确凭据
	body := strings.NewReader(`{"username":"admin","password":"pass123"}`)
	req := httptest.NewRequest("POST", "/api/login", body)
	w := httptest.NewRecorder()
	h.handleLogin(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", w.Code)
	}
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["code"].(float64) != 0 {
		t.Errorf("登录成功应返回 code=0，实际 %v", resp["code"])
	}

	// 错误凭据
	body = strings.NewReader(`{"username":"admin","password":"wrong"}`)
	req = httptest.NewRequest("POST", "/api/login", body)
	w = httptest.NewRecorder()
	h.handleLogin(w, req)
	var resp2 map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp2)
	if resp2["code"].(float64) == 0 {
		t.Error("错误凭据不应返回 code=0")
	}
}

// 测试 /api/login 与其余 API 一致受请求体大小限制（默认 GET 无明文私钥配套的边界用例见 TLS 测试）
func TestLoginRequestBodyLimited(t *testing.T) {
	srv := NewServer(Config{}, NewHandler(nil, nil, logger.NewTest(), []byte("secret"), "admin", "pass123", nil))
	mux := srv.buildRoutes()

	// 超过 4MB 上限的登录请求应被拒绝（MaxBytesReader 触发解码失败），而非无限读入内存
	big := strings.Repeat("a", maxRequestBody+1024)
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"`+big+`"}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Errorf("超大请求体不应成功处理: %s", w.Body.String())
	}
}

// 测试 GeoIP 下载接口的 SSRF 加固：拒绝非 https 与内网/回环目标，错误信息不回显内部细节
func TestGeoIPDownloadRejectsInternalURLs(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	cases := []string{
		"http://example.com/geoip.mmdb",  // 非 https
		"ftp://example.com/geoip.mmdb",   // 非 http(s)
		"https://127.0.0.1/geoip.mmdb",   // 回环 IPv4
		"https://[::1]/geoip.mmdb",       // 回环 IPv6
		"https://0.0.0.0/geoip.mmdb",     // 未指定地址
		"https://10.1.2.3/geoip.mmdb",    // 私网
		"https://192.168.1.1/geoip.mmdb", // 私网
		"https://172.16.0.9/geoip.mmdb",  // 私网
		"https://169.254.3.4/geoip.mmdb", // 链路本地
		"https://localhost/geoip.mmdb",   // 主机名解析为回环
	}
	for _, u := range cases {
		body := fmt.Sprintf(`{"url":%q}`, u)
		w := httptest.NewRecorder()
		h.handleGeoIPDownload(w, httptest.NewRequest(http.MethodPost, "/api/geoip/download", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("URL %q 应被拒绝（400），实际 %d: %s", u, w.Code, w.Body.String())
			continue
		}
		var resp map[string]interface{}
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Errorf("URL %q 错误响应应为 JSON: %v", u, err)
			continue
		}
		if c, _ := resp["code"].(float64); c != 400 {
			t.Errorf("URL %q 业务码期望 400，实际 %v", u, resp["code"])
		}
		// 错误信息不回显内部细节（如具体 IP / DNS 错误）
		if msg, _ := resp["message"].(string); strings.Contains(msg, "127.0.0.1") || strings.Contains(msg, "lookup") {
			t.Errorf("URL %q 错误信息不应回显内部细节: %s", u, msg)
		}
	}
}

// 测试 GET /api/web/tls 私钥脱敏：默认只返回 has_key/key_masked 摘要，reveal_key=1 才返回明文；PUT 不受影响
func TestWebTLSKeyMaskingAndReveal(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := generateTestCertPEM(t)
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, []byte(certPEM), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, []byte(keyPEM), 0600); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	h.tls = NewTLSStore(filepath.Join(dir, "web_tls.json"))

	// PUT 保存证书对（PUT 行为不变）
	putBody := fmt.Sprintf(`{"enabled":false,"cert_file":%q,"key_file":%q}`, certFile, keyFile)
	w := httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodPut, "/api/web/tls", strings.NewReader(putBody)))
	if w.Code != http.StatusOK {
		t.Fatalf("PUT 应成功，实际 %d: %s", w.Code, w.Body.String())
	}

	// 默认 GET：无 key_pem 明文，返回 has_key=true 与 key_masked 一行摘要
	w = httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodGet, "/api/web/tls", nil))
	var resp struct {
		Data struct {
			HasKey    bool   `json:"has_key"`
			KeyMasked string `json:"key_masked"`
			KeyPEM    string `json:"key_pem"`
			CertPEM   string `json:"cert_pem"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("解析默认 GET 响应失败: %v", err)
	}
	if !resp.Data.HasKey {
		t.Errorf("默认 GET 应返回 has_key=true")
	}
	if resp.Data.KeyPEM != "" {
		t.Errorf("默认 GET 不应返回私钥明文")
	}
	if !strings.Contains(resp.Data.KeyMasked, "BEGIN") || !strings.Contains(resp.Data.KeyMasked, "已隐藏") ||
		strings.Contains(resp.Data.KeyMasked, "\n") {
		t.Errorf("key_masked 应为单行 BEGIN ... （已隐藏）摘要，实际 %q", resp.Data.KeyMasked)
	}
	if strings.Contains(resp.Data.KeyMasked, keyPEM[:32]) {
		t.Errorf("key_masked 不应包含私钥内容片段")
	}

	// reveal_key=1：返回明文 key_pem
	w = httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodGet, "/api/web/tls?reveal_key=1", nil))
	resp = struct {
		Data struct {
			HasKey    bool   `json:"has_key"`
			KeyMasked string `json:"key_masked"`
			KeyPEM    string `json:"key_pem"`
			CertPEM   string `json:"cert_pem"`
		} `json:"data"`
	}{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("解析 reveal GET 响应失败: %v", err)
	}
	if !strings.Contains(resp.Data.KeyPEM, "PRIVATE KEY") || !strings.Contains(resp.Data.KeyPEM, "END") {
		t.Errorf("reveal_key=1 应返回私钥明文，实际 %q", resp.Data.KeyPEM)
	}

	// PUT 空证书/私钥内容表示保持不变：仅切换开关不应清掉已保存路径
	w = httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodPut, "/api/web/tls", strings.NewReader(`{"enabled":false}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("空内容 PUT 应成功（保持不变），实际 %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodGet, "/api/web/tls", nil))
	resp = struct {
		Data struct {
			HasKey    bool   `json:"has_key"`
			KeyMasked string `json:"key_masked"`
			KeyPEM    string `json:"key_pem"`
			CertPEM   string `json:"cert_pem"`
		} `json:"data"`
	}{}
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Data.HasKey || resp.Data.CertPEM == "" {
		t.Errorf("空内容 PUT 后已保存的证书对应保持不变（has_key=true 且证书仍可回读）")
	}
}

func TestWebStartupRequiresAuthenticationForRemoteBind(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "::", "192.0.2.10"} {
		h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
		s := NewServer(Config{Host: host, Port: -1}, h)
		if err := s.Start(); err == nil || !strings.Contains(err.Error(), "必须启用 WEB 访问密码") {
			t.Fatalf("地址 %s 应因缺少认证而拒绝启动，错误为 %v", host, err)
		}
	}
}

func TestWebStartupRejectsInvalidAuthAndTLS(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), nil, "admin", strings.Repeat("a", 73), nil)
	s := NewServer(Config{Host: "127.0.0.1", Port: -1}, h)
	if err := s.Start(); err == nil || !strings.Contains(err.Error(), "72 bytes") {
		t.Fatalf("无效的启动凭据应拒绝启动，错误为 %v", err)
	}

	h = NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	tlsPath := filepath.Join(t.TempDir(), "web_tls.json")
	if err := SaveWebTLSSettings(tlsPath, WebTLSSettings{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	h.tls = NewTLSStore(tlsPath)
	s = NewServer(Config{Host: "127.0.0.1", Port: -1}, h)
	if err := s.Start(); err == nil || !strings.Contains(err.Error(), "HTTPS 证书加载失败") {
		t.Fatalf("无效的证书应拒绝启动，错误为 %v", err)
	}
}

func TestWebCannotDisableAuthOnRemoteBind(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), nil, "admin", "pass123", nil)
	NewServer(Config{Host: "0.0.0.0"}, h)
	w := httptest.NewRecorder()
	h.handleAuthSettings(w, httptest.NewRequest("PUT", "/auth/settings", strings.NewReader(`{"enabled":false}`)))
	if w.Code != http.StatusForbidden || !h.AuthEnabled() {
		t.Fatalf("远程监听应拒绝关闭认证，状态码为 %d", w.Code)
	}
}

// ipCertPairPEM 生成指定 IP 的自签证书对（模拟已签发的 IP 证书）
func ipCertPairPEM(t *testing.T, ip string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(6 * 24 * time.Hour),
		IPAddresses:  []net.IP{net.ParseIP(ip)},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, &x509.Certificate{}, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("签发证书失败: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("序列化私钥失败: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

// TestWebTLSUseIPCert WEB 面板 HTTPS 可选用 IP 证书：
// 未签发时拒绝启用；签发后启用成功且监听配置动态取证书；关闭 IP 证书被拦截
func TestWebTLSUseIPCert(t *testing.T) {
	h := newCertTestHandler(t)
	m := ipcert.NewManager(t.TempDir(), nil)
	h.SetIPCert(m)

	// 未签发时启用 → 400
	w := httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodPut, "/api/web/tls",
		strings.NewReader(`{"enabled":true,"use_ipcert":true}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("IP 证书未签发时启用应返回 400, got %d: %s", w.Code, w.Body.String())
	}

	// 已签发 → 启用成功。通过磁盘证书文件 + ApplySettings 模拟已签发状态
	// （证书 IP 与设置一致且未到续期时间，不会触发真实签发）
	dir := t.TempDir()
	certPEM, keyPEM := ipCertPairPEM(t, "203.0.113.10")
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0600); err != nil {
		t.Fatalf("写入证书失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0600); err != nil {
		t.Fatalf("写入私钥失败: %v", err)
	}
	m = ipcert.NewManager(dir, nil)
	h.SetIPCert(m)
	if err := m.ApplySettings(ipcert.Settings{Enabled: true, IP: "203.0.113.10"}); err != nil {
		t.Fatalf("启用 IP 证书失败: %v", err)
	}

	w = httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodPut, "/api/web/tls",
		strings.NewReader(`{"enabled":true,"use_ipcert":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("已签发后启用应返回 200, got %d: %s", w.Code, w.Body.String())
	}

	// GET 回显 use_ipcert，且无需返回证书内容
	w = httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodGet, "/api/web/tls", nil))
	var got struct {
		Data struct {
			Enabled   bool   `json:"enabled"`
			UseIPCert bool   `json:"use_ipcert"`
			CertFile  string `json:"cert_file"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !got.Data.Enabled || !got.Data.UseIPCert || got.Data.CertFile != "" {
		t.Errorf("GET 应回显 use_ipcert 且无证书文件: %+v", got.Data)
	}

	// 监听配置为动态证书：GetCertificate 返回已签发的 IP 证书
	cfg, err := h.tls.listenerConfig()
	if err != nil || cfg == nil || cfg.GetCertificate == nil {
		t.Fatalf("listenerConfig 应返回动态证书配置: cfg=%v err=%v", cfg, err)
	}
	cert, err := cfg.GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate 不应失败: %v", err)
	}
	if cert.Leaf == nil {
		if cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0]); err != nil {
			t.Fatalf("解析证书失败: %v", err)
		}
	}
	if len(cert.Leaf.IPAddresses) == 0 || cert.Leaf.IPAddresses[0].String() != "203.0.113.10" {
		t.Errorf("监听应使用 IP 证书: %v", cert.Leaf.IPAddresses)
	}

	// 面板正在使用 IP 证书时，关闭 IP 证书被拦截
	w = httptest.NewRecorder()
	h.handleIPCert(w, httptest.NewRequest(http.MethodPut, "/api/settings/ipcert",
		strings.NewReader(`{"enabled":false}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("面板使用中关闭 IP 证书应返回 400, got %d: %s", w.Code, w.Body.String())
	}

	// 改用 PEM 证书后即可关闭 IP 证书
	certPEM2, keyPEM2 := generateTestCertPEM(t)
	w = httptest.NewRecorder()
	body, _ := json.Marshal(struct {
		Enabled bool   `json:"enabled"`
		CertPEM string `json:"cert_pem"`
		KeyPEM  string `json:"key_pem"`
	}{true, string(certPEM2), string(keyPEM2)})
	h.handleWebTLS(w, httptest.NewRequest(http.MethodPut, "/api/web/tls", strings.NewReader(string(body))))
	if w.Code != http.StatusOK {
		t.Fatalf("改用 PEM 证书应返回 200, got %d: %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.handleIPCert(w, httptest.NewRequest(http.MethodPut, "/api/settings/ipcert",
		strings.NewReader(`{"enabled":false}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("改用 PEM 后关闭 IP 证书应返回 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestWebTLSSwitchToIPCertKeepsPEMFiles 从 PEM 切换到 IP 证书时保留已部署证书文件，
// 便于切回 PEM 模式时无需重新填写（也避免误删 certd 部署的证书）
func TestWebTLSSwitchToIPCertKeepsPEMFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	h.tls = NewTLSStore(filepath.Join(t.TempDir(), "web_tls.json"))

	certPEM, keyPEM := generateTestCertPEM(t)
	saved, err := h.tls.SavePEM(true, certPEM, keyPEM)
	if err != nil {
		t.Fatalf("保存 PEM 证书失败: %v", err)
	}

	m := ipcert.NewManager(t.TempDir(), nil)
	h.SetIPCert(m)

	// 直接把签发结果写入 IP 证书目录（模拟已签发），再换成该管理器
	ipPEM, ipKeyPEM := ipCertPairPEM(t, "203.0.113.10")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), ipPEM, 0600); err != nil {
		t.Fatalf("写入证书失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), ipKeyPEM, 0600); err != nil {
		t.Fatalf("写入私钥失败: %v", err)
	}
	m = ipcert.NewManager(dir, nil)
	h.SetIPCert(m)
	if err := m.ApplySettings(ipcert.Settings{Enabled: true, IP: "203.0.113.10"}); err != nil {
		t.Fatalf("启用 IP 证书失败: %v", err)
	}

	w := httptest.NewRecorder()
	h.handleWebTLS(w, httptest.NewRequest(http.MethodPut, "/api/web/tls",
		strings.NewReader(`{"enabled":true,"use_ipcert":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("切换到 IP 证书应成功, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(saved.CertFile); err != nil {
		t.Errorf("切换后原 PEM 证书文件应保留: %v", err)
	}
	if _, err := os.Stat(saved.KeyFile); err != nil {
		t.Errorf("切换后原 PEM 私钥文件应保留: %v", err)
	}
	if got := h.tls.Settings(); got.CertFile != saved.CertFile || !got.UseIPCert {
		t.Errorf("设置应保留证书文件并启用 IP 证书: %+v", got)
	}
}

// TestWebTLSUseIPCertAfterPersistentAuth 回归：main.go 的顺序是先 SetIPCert 再 UsePersistentAuth，
// 后者会用持久化路径重建 TLSStore；IP 证书来源必须重新挂到新实例，
// 否则 WEB 模式下「面板 HTTPS 使用 IP 证书」会报「IP 证书功能不可用」
func TestWebTLSUseIPCertAfterPersistentAuth(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	certPEM, keyPEM := ipCertPairPEM(t, "203.0.113.10")
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0600); err != nil {
		t.Fatalf("写入证书失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0600); err != nil {
		t.Fatalf("写入私钥失败: %v", err)
	}
	m := ipcert.NewManager(dir, nil)
	if err := m.ApplySettings(ipcert.Settings{Enabled: true, IP: "203.0.113.10"}); err != nil {
		t.Fatalf("启用 IP 证书失败: %v", err)
	}

	// 与 main.go 相同顺序：先注入 IP 证书，再加载持久化设置（会替换 TLSStore）
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	h.SetIPCert(m)
	if err := h.UsePersistentAuth("", ""); err != nil {
		t.Fatalf("加载持久化设置失败: %v", err)
	}
	if err := h.tls.Save(WebTLSSettings{Enabled: true, UseIPCert: true}); err != nil {
		t.Fatalf("保存使用 IP 证书的面板设置失败: %v", err)
	}
	cfg, err := h.tls.listenerConfig()
	if err != nil {
		t.Fatalf("持久化后监听配置不应失败: %v", err)
	}
	if cfg == nil || cfg.GetCertificate == nil {
		t.Fatalf("应返回动态证书配置: %+v", cfg)
	}
	if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{}); err != nil {
		t.Fatalf("证书来源应可用: %v", err)
	}
}

// newIssuedIPCertManager 创建已签发 IP 证书的管理器，并返回证书 PEM 以便断言
func newIssuedIPCertManager(t *testing.T, ip string) (*ipcert.Manager, []byte) {
	t.Helper()
	dir := t.TempDir()
	certPEM, keyPEM := ipCertPairPEM(t, ip)
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0600); err != nil {
		t.Fatalf("写入证书失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0600); err != nil {
		t.Fatalf("写入私钥失败: %v", err)
	}
	m := ipcert.NewManager(dir, nil)
	if err := m.ApplySettings(ipcert.Settings{Enabled: true, IP: ip}); err != nil {
		t.Fatalf("启用 IP 证书失败: %v", err)
	}
	return m, certPEM
}

// TestIPCertDisableBlockedByTunnelListener 隧道 HTTPS 监听器使用 IP 证书时，
// 关闭 IP 证书应被拦截；改用 PEM 后允许关闭
func TestIPCertDisableBlockedByTunnelListener(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfgMgr, err := config.NewManager()
	if err != nil {
		t.Fatalf("创建配置管理失败: %v", err)
	}
	cfg := config.TunnelConfig{
		Name:       "IP 证书隧道",
		HopChain:   []config.HopConfig{{User: "u", Host: "127.0.0.1", Port: 22, AuthType: "password"}},
		TunnelType: config.TunnelProxy,
		ProxyListeners: []config.ProxyListener{{
			ID: "https-ip", Protocol: config.ProxyHTTPS, ListenPort: 18443,
			TLS: &config.TLSConfig{UseIPCert: true},
		}},
	}
	if err := cfgMgr.Save(&cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	h := NewHandler(cfgMgr, nil, logger.NewTest(), nil, "", "", nil)
	m, _ := newIssuedIPCertManager(t, "203.0.113.10")
	h.SetIPCert(m)

	w := httptest.NewRecorder()
	h.handleIPCert(w, httptest.NewRequest(http.MethodPut, "/api/settings/ipcert",
		strings.NewReader(`{"enabled":false}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("监听器使用中关闭 IP 证书应返回 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "IP 证书隧道") {
		t.Errorf("错误信息应指出使用方: %s", w.Body.String())
	}

	// 监听器改用 PEM 证书后允许关闭
	cfg.ProxyListeners[0].TLS = &config.TLSConfig{CertPEM: "x", KeyPEM: "y"}
	if err := cfgMgr.Save(&cfg); err != nil {
		t.Fatalf("更新配置失败: %v", err)
	}
	w = httptest.NewRecorder()
	h.handleIPCert(w, httptest.NewRequest(http.MethodPut, "/api/settings/ipcert",
		strings.NewReader(`{"enabled":false}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("改用 PEM 后应允许关闭, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCertReadReturnsActiveIPCert IP 证书模式下 certd 读取面板/监听器证书，
// 应返回当前实际生效的 IP 证书，而不是保留的旧 PEM
func TestCertReadReturnsActiveIPCert(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	cfgMgr, err := config.NewManager()
	if err != nil {
		t.Fatalf("创建配置管理失败: %v", err)
	}
	h.cfgMgr = cfgMgr
	m, ipPEM := newIssuedIPCertManager(t, "203.0.113.10")
	h.SetIPCert(m)
	h.tls = NewTLSStore(filepath.Join(t.TempDir(), "web_tls.json"))
	h.tls.SetIPCertProvider(m.TLSCertificate)

	// 面板：保留旧 PEM 文件，但处于 IP 证书模式
	oldPEM, oldKey := generateTestCertPEM(t)
	if _, err := h.tls.SavePEM(true, oldPEM, oldKey); err != nil {
		t.Fatalf("保存旧证书失败: %v", err)
	}
	if err := h.tls.Save(WebTLSSettings{Enabled: true, UseIPCert: true}); err != nil {
		t.Fatalf("启用 IP 证书模式失败: %v", err)
	}
	_, certPEM, _, err := h.resolveTargetPEM(CertTargetWeb, "", "")
	if err != nil {
		t.Fatalf("读取面板证书失败: %v", err)
	}
	if certPEM != string(ipPEM) {
		t.Errorf("面板应返回生效的 IP 证书")
	}

	// 隧道监听器：同样返回 IP 证书
	cfg := config.TunnelConfig{
		Name:       "t",
		HopChain:   []config.HopConfig{{User: "u", Host: "127.0.0.1", Port: 22, AuthType: "password"}},
		TunnelType: config.TunnelProxy,
		ProxyListeners: []config.ProxyListener{{
			ID: "https-ip", Protocol: config.ProxyHTTPS, ListenPort: 18443,
			TLS: &config.TLSConfig{UseIPCert: true},
		}},
	}
	if err := cfgMgr.Save(&cfg); err != nil {
		t.Fatalf("保存配置失败: %v", err)
	}
	_, certPEM, _, err = h.resolveTargetPEM(CertTargetTunnel, cfg.ID, "https-ip")
	if err != nil {
		t.Fatalf("读取监听器证书失败: %v", err)
	}
	if certPEM != string(ipPEM) {
		t.Errorf("监听器应返回生效的 IP 证书")
	}
}

// TestCertDeployWebReportsIPCertSwitch certd 部署面板证书后切回 PEM 模式，
// 响应需明确告知此前处于 IP 证书模式（不再静默切换）
func TestCertDeployWebReportsIPCertSwitch(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	h.tls = NewTLSStore(filepath.Join(t.TempDir(), "web_tls.json"))
	if err := h.tls.Save(WebTLSSettings{Enabled: true, UseIPCert: true}); err != nil {
		t.Fatalf("启用 IP 证书模式失败: %v", err)
	}

	certPEM, keyPEM := generateTestCertPEM(t)
	body, _ := json.Marshal(map[string]interface{}{
		"target": "web", "cert_pem": certPEM, "key_pem": keyPEM, "restart": false,
	})
	w := httptest.NewRecorder()
	h.handleCertDeploy(w, httptest.NewRequest(http.MethodPost, "/cert/deploy", strings.NewReader(string(body))))
	if w.Code != http.StatusOK {
		t.Fatalf("部署失败: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			UseIPCert          bool `json:"use_ipcert"`
			SwitchedFromIPCert bool `json:"switched_from_ipcert"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Data.UseIPCert || !resp.Data.SwitchedFromIPCert {
		t.Errorf("应报告已从 IP 证书切换到 PEM: %+v", resp.Data)
	}
	if h.tls.Settings().UseIPCert {
		t.Errorf("部署后面板应切换到 PEM 模式")
	}
}
