package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sshtunnel/internal/logger"
)

// 测试鉴权状态接口（无密码模式）
func TestAuthStatusDisabled(t *testing.T) {
	srv := NewServer(Config{AuthEnabled: false}, NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil))
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
	mw := authMiddleware(false, []byte("secret"))
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
	mw := authMiddleware(true, []byte("secret"))
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
	mw := authMiddleware(true, secret)
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
	mw := authMiddleware(true, []byte("secret"))
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
