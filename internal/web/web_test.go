package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
		} `json:"data"`
	}{}
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Data.CertPEM == "" || !strings.Contains(resp.Data.CertPEM, "BEGIN CERTIFICATE") {
		t.Errorf("GET 应回读证书 PEM 内容")
	}
	if resp.Data.KeyPEM == "" || !strings.Contains(resp.Data.KeyPEM, "PRIVATE KEY") {
		t.Errorf("GET 应回读私钥 PEM 内容")
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
	mw := authMiddleware(func() bool { return false }, []byte("secret"))
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
	mw := authMiddleware(func() bool { return true }, []byte("secret"))
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
	mw := authMiddleware(func() bool { return true }, secret)
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
	mw := authMiddleware(func() bool { return true }, []byte("secret"))
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
