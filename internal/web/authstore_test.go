package web

import (
	"bytes"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sshtunnel/internal/logger"
)

// TestAuthStoreDefaultDisabled 验证默认不开启访问密码
func TestAuthStoreDefaultDisabled(t *testing.T) {
	s := NewAuthStore("")
	if s.Enabled() {
		t.Errorf("默认应为未启用状态")
	}
	if s.Verify("admin", "anything") {
		t.Errorf("未启用时不应通过校验")
	}
}

// TestAuthStoreEnableVerify 验证开启后校验凭据
func TestAuthStoreEnableVerify(t *testing.T) {
	s := NewAuthStore("")
	if err := s.Set("admin", "pass123"); err != nil {
		t.Fatalf("开启失败: %v", err)
	}
	if !s.Enabled() {
		t.Errorf("Set 后应为启用状态")
	}
	if !s.Verify("admin", "pass123") {
		t.Errorf("正确凭据应通过")
	}
	if s.Verify("admin", "wrong") {
		t.Errorf("错误密码不应通过")
	}
	if s.Verify("other", "pass123") {
		t.Errorf("错误用户名不应通过")
	}
}

// TestAuthStoreValidation 验证参数校验
func TestAuthStoreValidation(t *testing.T) {
	s := NewAuthStore("")
	if err := s.Set("", "pass123"); err == nil {
		t.Errorf("空用户名应报错")
	}
	if err := s.Set("ad:min", "pass123"); err == nil {
		t.Errorf("含冒号的用户名应报错")
	}
	if err := s.Set("admin", "12345"); err == nil {
		t.Errorf("密码少于 6 位应报错")
	}
}

// TestAuthStorePersistence 验证持久化与重新加载
func TestAuthStorePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	s1 := NewAuthStore(path)
	if err := s1.Set("admin", "pass123"); err != nil {
		t.Fatalf("开启失败: %v", err)
	}

	// 重新加载：应保持启用且凭据有效
	s2 := NewAuthStore(path)
	if !s2.Enabled() {
		t.Errorf("重新加载后应保持启用")
	}
	if !s2.Verify("admin", "pass123") {
		t.Errorf("重新加载后凭据应有效")
	}
	if s2.Username() != "admin" {
		t.Errorf("用户名不匹配: %q", s2.Username())
	}
}

// TestAuthStoreDisable 验证关闭后放行，且再次开启可沿用密码
func TestAuthStoreDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s := NewAuthStore(path)
	if err := s.Set("admin", "pass123"); err != nil {
		t.Fatalf("开启失败: %v", err)
	}
	if err := s.Disable(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}
	if s.Enabled() {
		t.Errorf("Disable 后应为未启用")
	}
	if s.Verify("admin", "pass123") {
		t.Errorf("关闭后不应通过校验")
	}

	// 关闭状态持久化：重新加载仍为关闭
	s2 := NewAuthStore(path)
	if s2.Enabled() {
		t.Errorf("重新加载后应保持关闭")
	}

	// 再次开启，留空密码沿用旧密码
	if err := s2.Set("admin", ""); err != nil {
		t.Fatalf("沿用密码再开启失败: %v", err)
	}
	if !s2.Verify("admin", "pass123") {
		t.Errorf("沿用密码再开启后凭据应有效")
	}
}

// TestAuthStoreChangePassword 验证修改密码
func TestAuthStoreChangePassword(t *testing.T) {
	s := NewAuthStore("")
	if err := s.Set("admin", "pass123"); err != nil {
		t.Fatalf("开启失败: %v", err)
	}
	if err := s.Set("admin", "newpass456"); err != nil {
		t.Fatalf("修改密码失败: %v", err)
	}
	if s.Verify("admin", "pass123") {
		t.Errorf("旧密码不应再通过")
	}
	if !s.Verify("admin", "newpass456") {
		t.Errorf("新密码应通过")
	}
}

// TestAuthStoreSecretPersistence 验证 JWT 密钥持久化（重启后 Token 仍有效）
func TestAuthStoreSecretPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	s1 := NewAuthStore(path)
	sec1 := string(s1.Secret())
	if sec1 == "" {
		t.Fatalf("Secret 不应为空")
	}

	// 重新加载：应得到相同密钥
	s2 := NewAuthStore(path)
	sec2 := string(s2.Secret())
	if sec1 != sec2 {
		t.Errorf("重启后密钥应保持一致: %q vs %q", sec1, sec2)
	}

	// 内存模式：每次新实例各自随机（不落盘）
	if string(NewAuthStore("").Secret()) == string(NewAuthStore("").Secret()) {
		t.Errorf("内存模式下密钥不应相同")
	}
}

// TestAuthStoreCorruptSettings 验证 settings.json 损坏时：输出 warning、按未启用处理且不 panic；
// 重新保存后文件被原子写恢复（无残留临时文件、权限 0600）
func TestAuthStoreCorruptSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// 模拟中断写坏的残缺文件
	if err := os.WriteFile(path, []byte(`{"auth_enabled":true,"username":"admin","pass`), 0600); err != nil {
		t.Fatal(err)
	}

	// 捕获标准 log 输出，确认解析失败不再静默
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	s := NewAuthStore(path)
	if s.Enabled() {
		t.Errorf("损坏的配置应按未启用处理")
	}
	log.SetOutput(prev) // 先恢复，避免后续持久化日志混入
	if !strings.Contains(buf.String(), "加载 WEB 访问密码设置失败") {
		t.Errorf("解析失败应输出 warning 日志，实际: %q", buf.String())
	}

	// 重新设置后应覆盖残缺文件且为原子写：目录无 .tmp 残留
	if err := s.Set("admin", "pass123"); err != nil {
		t.Fatalf("重新启用失败: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("原子写不应残留临时文件: %s", e.Name())
		}
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0600 {
		t.Errorf("settings.json 权限 = %o, want 600", perm)
	}
	// 恢复后的文件可正常重新加载
	if s2 := NewAuthStore(path); !s2.Enabled() || s2.Username() != "admin" {
		t.Errorf("恢复后的配置应可正常加载")
	}
}

// TestAuthSettingsEndpoint 验证 /api/auth/settings 接口
func TestAuthSettingsEndpoint(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), []byte("secret"), "", "", nil)

	// GET：默认未启用
	req := httptest.NewRequest("GET", "/auth/settings", nil)
	w := httptest.NewRecorder()
	h.handleAuthSettings(w, req)
	if w.Code != 200 {
		t.Fatalf("GET 期望 200，实际 %d", w.Code)
	}

	// PUT：开启并设置凭据，应返回 token
	body := `{"enabled":true,"username":"admin","password":"pass123"}`
	w = httptest.NewRecorder()
	h.handleAuthSettings(w, httptest.NewRequest("PUT", "/auth/settings", strings.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("PUT 期望 200，实际 %d: %s", w.Code, w.Body.String())
	}
	if !h.AuthEnabled() {
		t.Errorf("PUT 后应处于启用状态")
	}

	// PUT：非法密码应 400
	w = httptest.NewRecorder()
	h.handleAuthSettings(w, httptest.NewRequest("PUT", "/auth/settings",
		strings.NewReader(`{"enabled":true,"username":"admin","password":"12"}`)))
	if w.Code != 400 {
		t.Errorf("短密码期望 400，实际 %d", w.Code)
	}

	// PUT：关闭
	w = httptest.NewRecorder()
	h.handleAuthSettings(w, httptest.NewRequest("PUT", "/auth/settings",
		strings.NewReader(`{"enabled":false}`)))
	if w.Code != 200 || h.AuthEnabled() {
		t.Errorf("关闭失败: code=%d enabled=%v", w.Code, h.AuthEnabled())
	}
}
