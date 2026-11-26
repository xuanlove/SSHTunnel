package config

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var _ = os.ReadFile

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	// 使用临时目录避免污染真实配置
	tmpDir := t.TempDir()
	cfgDir := filepath.Join(tmpDir, "sshtunnel")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}

	m, err := newManagerWithPath(filepath.Join(cfgDir, "configs.json"))
	if err != nil {
		t.Fatalf("创建测试 Manager 失败: %v", err)
	}
	return m
}

func TestManagerSaveAndGet(t *testing.T) {
	m := newTestManager(t)

	cfg := TunnelConfig{
		Name:       "测试隧道",
		HopChain:   []HopConfig{{User: "u", Host: "h", Port: 22, AuthType: "password", Password: "p"}},
		TunnelType: TunnelProxy,
		ProxyListeners: []ProxyListener{
			{ID: "l1", Protocol: ProxySOCKS5, ListenPort: 1080, AllowExternal: false},
		},
		AutoReconnect: true,
	}

	if err := m.Save(&cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	// Save 使用指针接收者，ID 应已回填到 cfg
	if cfg.ID == "" {
		t.Errorf("Save 后 cfg.ID 应已回填")
	}

	list := m.List()
	if len(list) != 1 {
		t.Fatalf("保存后 List 长度应为 1, got %d", len(list))
	}
	saved := list[0]
	if saved.ID == "" {
		t.Errorf("ID 应已自动生成")
	}
	if saved.Name != cfg.Name {
		t.Errorf("Name 不匹配: got %q, want %q", saved.Name, cfg.Name)
	}
	if saved.Status != StatusStopped {
		t.Errorf("Status 应为 stopped, got %q", saved.Status)
	}
	if len(saved.ProxyListeners) != 1 {
		t.Errorf("ProxyListeners 长度应为 1")
	}

	// 通过 Get 获取
	got, ok := m.Get(saved.ID)
	if !ok {
		t.Fatalf("Get 失败")
	}
	if got.Name != cfg.Name {
		t.Errorf("Get 返回的 Name 不匹配")
	}
}

func TestManagerList(t *testing.T) {
	m := newTestManager(t)

	for i := 0; i < 3; i++ {
		_ = m.Save(&TunnelConfig{Name: "t" + string(rune('1'+i))})
	}

	list := m.List()
	if len(list) != 3 {
		t.Errorf("List 长度应为 3, got %d", len(list))
	}
}

func TestManagerListKeepsCreationOrder(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "configs.json")
	m, err := newManagerWithPath(cfgPath)
	if err != nil {
		t.Fatalf("创建 Manager 失败: %v", err)
	}

	// 依次创建 5 条，记录 ID 顺序
	var ids []string
	for i := 0; i < 5; i++ {
		c := &TunnelConfig{Name: "t" + string(rune('1'+i))}
		if err := m.Save(c); err != nil {
			t.Fatalf("保存失败: %v", err)
		}
		ids = append(ids, c.ID)
	}

	// 多次读取顺序必须完全一致（map 遍历无序，此前每次可能不同）
	for round := 0; round < 20; round++ {
		list := m.List()
		if len(list) != len(ids) {
			t.Fatalf("List 长度应为 %d, got %d", len(ids), len(list))
		}
		for i, c := range list {
			if c.ID != ids[i] {
				t.Fatalf("第 %d 轮顺序不一致: 位置 %d 期望 %s, got %s", round, i, ids[i], c.ID)
			}
		}
	}

	// 编辑不应改变位置
	if err := m.Save(&TunnelConfig{ID: ids[2], Name: "renamed"}); err != nil {
		t.Fatalf("编辑失败: %v", err)
	}
	list := m.List()
	for i, c := range list {
		if c.ID != ids[i] {
			t.Fatalf("编辑后顺序变化: 位置 %d 期望 %s, got %s", i, ids[i], c.ID)
		}
	}

	// 删除中间一条后，其余保持相对顺序
	if err := m.Delete(ids[1]); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	want := []string{ids[0], ids[2], ids[3], ids[4]}
	list = m.List()
	for i, c := range list {
		if c.ID != want[i] {
			t.Fatalf("删除后顺序错误: 位置 %d 期望 %s, got %s", i, want[i], c.ID)
		}
	}

	// 重新加载后顺序保持一致（持久化顺序稳定）
	m2, err := newManagerWithPath(cfgPath)
	if err != nil {
		t.Fatalf("重新加载失败: %v", err)
	}
	list = m2.List()
	for i, c := range list {
		if c.ID != want[i] {
			t.Fatalf("重启后顺序错误: 位置 %d 期望 %s, got %s", i, want[i], c.ID)
		}
	}
}

func TestManagerDelete(t *testing.T) {
	m := newTestManager(t)
	_ = m.Save(&TunnelConfig{Name: "t1"})

	list := m.List()
	if len(list) != 1 {
		t.Fatalf("初始应有 1 个配置")
	}

	if err := m.Delete(list[0].ID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if len(m.List()) != 0 {
		t.Errorf("删除后应为 0")
	}

	// 重复删除应报错
	if err := m.Delete(list[0].ID); err == nil {
		t.Errorf("删除不存在的配置应报错")
	}
}

func TestManagerPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	cfgDir := filepath.Join(tmpDir, "sshtunnel")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "configs.json")

	// 第一个 Manager 写入
	m1, err := newManagerWithPath(cfgPath)
	if err != nil {
		t.Fatalf("创建 m1 失败: %v", err)
	}
	_ = m1.Save(&TunnelConfig{Name: "持久化测试", AutoReconnect: true, AutoStart: true})

	// 第二个 Manager 从同一文件加载
	m2, err := newManagerWithPath(cfgPath)
	if err != nil {
		t.Fatalf("创建 m2 失败: %v", err)
	}
	list := m2.List()
	if len(list) != 1 {
		t.Fatalf("重新加载后应有 1 个配置, got %d", len(list))
	}
	if list[0].Name != "持久化测试" {
		t.Errorf("Name 不匹配: got %q", list[0].Name)
	}
	if list[0].Status != StatusStopped {
		t.Errorf("加载后 Status 应为 stopped, got %q", list[0].Status)
	}
	if !list[0].AutoStart {
		t.Errorf("加载后 AutoStart 应保持 true")
	}
}

func TestManagerSetStatus(t *testing.T) {
	m := newTestManager(t)
	_ = m.Save(&TunnelConfig{Name: "t1"})

	list := m.List()
	id := list[0].ID
	m.SetStatus(id, StatusRunning)

	cfg, _ := m.Get(id)
	if cfg.Status != StatusRunning {
		t.Errorf("Status 应为 running, got %q", cfg.Status)
	}
}

// TestTLSFileMigration 旧版 cert_file/key_file 配置应自动迁移为 PEM 文本
func TestTLSFileMigration(t *testing.T) {
	dir := t.TempDir()
	certDir := filepath.Join(dir, "certs")
	if err := os.Mkdir(certDir, 0700); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(certDir, "c.pem"), filepath.Join(certDir, "k.pem")
	server := httptest.NewTLSServer(nil)
	t.Cleanup(server.Close)
	pair := server.TLS.Certificates[0]
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pair.Certificate[0]})
	keyDER, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(dir, "outside.pem")
	if err := os.WriteFile(outsidePath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	outside := []ProxyListener{{TLS: &TLSConfig{CertFile: certPath, KeyFile: outsidePath}}}
	migrateTLSFromFiles(outside, dir)
	if result := outside[0].TLS; *result != (TLSConfig{}) {
		t.Fatal("迁移不得返回证书目录以外的文件")
	}
	cfgPath := filepath.Join(dir, "configs.json")

	// 模拟旧版配置文件（只含文件路径）
	legacy := []map[string]interface{}{{
		"id":   "t1",
		"name": "迁移测试",
		"proxy_listeners": []interface{}{map[string]interface{}{
			"id": "l1", "protocol": "https", "listen_port": 8443,
			"tls": map[string]string{"cert_file": certPath, "key_file": keyPath},
		}},
	}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("构造旧配置失败: %v", err)
	}
	if err := os.WriteFile(cfgPath, raw, 0644); err != nil {
		t.Fatalf("写入旧配置失败: %v", err)
	}

	m, err := newManagerWithPath(cfgPath)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	cfg, ok := m.Get("t1")
	if !ok || len(cfg.ProxyListeners) != 1 || cfg.ProxyListeners[0].TLS == nil {
		t.Fatalf("配置结构异常: %+v", cfg)
	}
	tlsCfg := cfg.ProxyListeners[0].TLS
	if tlsCfg.CertPEM != string(certPEM) || tlsCfg.KeyPEM != string(keyPEM) {
		t.Errorf("未迁移为 PEM 文本: %+v", tlsCfg)
	}
	if tlsCfg.CertFile != "" || tlsCfg.KeyFile != "" {
		t.Errorf("迁移后应清空旧路径字段: %+v", tlsCfg)
	}

	cfg.ProxyListeners[0].TLS = &TLSConfig{CertFile: certPath, KeyFile: keyPath}
	if err := m.Save(cfg); err != ErrLegacyTLSPaths {
		t.Fatalf("拒绝旧路径字段，实际错误为 %v", err)
	}
	cfg.ProxyListeners[0].TLS = tlsCfg

	// 新保存的配置不应再写 cert_file/key_file
	if err := m.Save(cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("读取保存结果失败: %v", err)
	}
	var list []TunnelConfig
	if err := json.Unmarshal(saved, &list); err != nil {
		t.Fatalf("解析保存结果失败: %v", err)
	}
	if len(list) != 1 || list[0].ProxyListeners[0].TLS.CertPEM != string(certPEM) {
		t.Errorf("保存结果未包含 PEM: %s", string(saved))
	}
	if string(saved) != "" && json.Valid(saved) {
		var rawList []map[string]interface{}
		_ = json.Unmarshal(saved, &rawList)
		if l, ok := rawList[0]["proxy_listeners"].([]interface{}); ok {
			tlsObj := l[0].(map[string]interface{})["tls"].(map[string]interface{})
			if _, exists := tlsObj["cert_file"]; exists {
				t.Errorf("新配置不应包含 cert_file 字段: %v", tlsObj)
			}
		}
	}
}

// TestConfigFilePermission 配置文件应以 0600 原子写入
func TestConfigFilePermission(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 权限模型不同")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "configs.json")
	m, err := newManagerWithPath(path)
	if err != nil {
		t.Fatalf("创建 Manager 失败: %v", err)
	}
	cfg := &TunnelConfig{Name: "permission"}
	if err := m.Save(cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("读取文件信息失败: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("配置文件权限 = %o, want 600", perm)
	}
}

// TestConfigGetReturnsCopy Get 应返回副本，外部修改不影响内部状态
func TestConfigGetReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	m, err := newManagerWithPath(filepath.Join(dir, "configs.json"))
	if err != nil {
		t.Fatalf("创建 Manager 失败: %v", err)
	}
	cfg := &TunnelConfig{Name: "copy"}
	if err := m.Save(cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, ok := m.Get(cfg.ID)
	if !ok {
		t.Fatalf("未找到配置")
	}
	got.Name = "changed"
	again, _ := m.Get(cfg.ID)
	if again.Name != "copy" {
		t.Errorf("Get 应返回副本，内部名称被修改为 %q", again.Name)
	}
}
