package config

import (
	"os"
	"path/filepath"
	"testing"
)

var _ = os.ReadFile

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	// 使用临时目录避免污染真实配置
	tmpDir := t.TempDir()
	cfgDir := filepath.Join(tmpDir, "sshsuidao")
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
		Name:          "测试隧道",
		HopChain:      []HopConfig{{User: "u", Host: "h", Port: 22, AuthType: "password", Password: "p"}},
		TunnelType:    TunnelProxy,
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
	cfgDir := filepath.Join(tmpDir, "sshsuidao")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}
	cfgPath := filepath.Join(cfgDir, "configs.json")

	// 第一个 Manager 写入
	m1, err := newManagerWithPath(cfgPath)
	if err != nil {
		t.Fatalf("创建 m1 失败: %v", err)
	}
	_ = m1.Save(&TunnelConfig{Name: "持久化测试", AutoReconnect: true})

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
