package alert

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sshtunnel/internal/logger"
	"sshtunnel/internal/tunnel"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New(nil, t.TempDir(), nil, nil, nil)
	if err != nil {
		t.Fatalf("创建告警引擎失败: %v", err)
	}
	return e
}

func TestRuleCRUD(t *testing.T) {
	e := newTestEngine(t)
	r, err := e.SaveRule(Rule{Type: TypeTunnelDown, TunnelID: "t1", Enabled: true})
	if err != nil {
		t.Fatalf("保存规则失败: %v", err)
	}
	if r.ID == "" || r.Name == "" {
		t.Errorf("应自动生成 ID 与名称: %+v", r)
	}
	if len(e.Rules()) != 1 {
		t.Errorf("规则数 = %d, want 1", len(e.Rules()))
	}

	if _, err := e.SaveRule(Rule{Type: TypeConnThreshold, Threshold: 0}); err == nil {
		t.Errorf("阈值为 0 应校验失败")
	}
	if _, err := e.SaveRule(Rule{Type: "unknown"}); err == nil {
		t.Errorf("未知类型应校验失败")
	}

	if err := e.DeleteRule(r.ID); err != nil {
		t.Errorf("删除规则失败: %v", err)
	}
	if len(e.Rules()) != 0 {
		t.Errorf("删除后规则数 = %d, want 0", len(e.Rules()))
	}
}

func TestWebhookValidation(t *testing.T) {
	e := newTestEngine(t)
	if err := e.SaveWebhook(Webhook{Enabled: true, URL: "ftp://x"}); err == nil {
		t.Errorf("非 http(s) 地址应校验失败")
	}
	if err := e.SaveWebhook(Webhook{Enabled: true, URL: "https://example.com/hook", Format: "wecom"}); err != nil {
		t.Errorf("保存 Webhook 失败: %v", err)
	}
	if got := e.Webhook(); got.URL != "https://example.com/hook" || got.Format != "wecom" {
		t.Errorf("Webhook 读取不符: %+v", got)
	}
}

func TestApplyCooldownAndResolve(t *testing.T) {
	e := &Engine{
		states:   map[string]*ruleState{},
		notifyCh: make(chan Notification, 8),
		hist:     []History{},
	}
	rule := Rule{ID: "r1", Name: "连接数", Type: TypeConnThreshold, Threshold: 10, Cooldown: 60}
	now := time.Now()

	e.apply(rule, true, 15, now)
	if len(e.hist) != 1 || e.hist[0].Status != StatusFiring {
		t.Fatalf("首次触发应产生 firing 记录: %+v", e.hist)
	}

	// 冷却期内不重复
	e.apply(rule, true, 16, now.Add(time.Second))
	if len(e.hist) != 1 {
		t.Errorf("冷却期内不应重复告警: %d", len(e.hist))
	}

	// 恢复
	e.apply(rule, false, 3, now.Add(2*time.Second))
	if len(e.hist) != 2 || e.hist[1].Status != StatusResolved {
		t.Errorf("恢复应产生 resolved 记录: %+v", e.hist)
	}

	// 冷却结束后再次触发
	e.apply(rule, true, 20, now.Add(61*time.Second))
	if len(e.hist) != 3 || e.hist[2].Status != StatusFiring {
		t.Errorf("冷却结束后应再次告警: %+v", e.hist)
	}
}

func TestApplyDurationWindow(t *testing.T) {
	e := &Engine{states: map[string]*ruleState{}, notifyCh: make(chan Notification, 4), hist: []History{}}
	rule := Rule{ID: "r2", Type: TypeConnThreshold, Threshold: 1, Duration: 30}
	now := time.Now()
	e.apply(rule, true, 5, now)
	if len(e.hist) != 0 {
		t.Errorf("未达到持续窗口不应告警: %+v", e.hist)
	}
	e.apply(rule, true, 5, now.Add(31*time.Second))
	if len(e.hist) != 1 || e.hist[0].Status != StatusFiring {
		t.Errorf("持续窗口结束后应告警: %+v", e.hist)
	}
}

func TestSendNotificationFormats(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := Notification{
		Rule:    Rule{ID: "r1", Name: "测试"},
		History: History{Status: StatusFiring, Message: "告警内容"},
	}
	if err := SendNotification(srv.Client(), Webhook{Enabled: true, URL: srv.URL, Format: "wecom"}, n); err != nil {
		t.Fatalf("发送企业微信通知失败: %v", err)
	}
	if got["msgtype"] != "markdown" {
		t.Errorf("企业微信 payload 不符: %+v", got)
	}

	if err := SendNotification(srv.Client(), Webhook{Enabled: true, URL: srv.URL, Format: "feishu"}, n); err != nil {
		t.Fatalf("发送飞书通知失败: %v", err)
	}
	if got["msg_type"] != "text" {
		t.Errorf("飞书 payload 不符: %+v", got)
	}

	if err := SendNotification(srv.Client(), Webhook{Enabled: true, URL: srv.URL}, n); err != nil {
		t.Fatalf("发送通用通知失败: %v", err)
	}
	if _, ok := got["history"]; !ok {
		t.Errorf("通用 payload 应包含 history: %+v", got)
	}
}

// TestApplyZeroCooldownNotifyOnce 冷却为 0 时持续触发仅通知一次，恢复后再次触发重新告警
func TestApplyZeroCooldownNotifyOnce(t *testing.T) {
	e := &Engine{states: map[string]*ruleState{}, notifyCh: make(chan Notification, 8), hist: []History{}}
	rule := Rule{ID: "r0", Name: "测试", Type: TypeConnThreshold, Threshold: 1, Cooldown: 0}
	now := time.Now()

	e.apply(rule, true, 5, now)
	e.apply(rule, true, 6, now.Add(10*time.Second))
	e.apply(rule, true, 7, now.Add(time.Minute))
	e.mu.Lock()
	n := len(e.hist)
	e.mu.Unlock()
	if n != 1 {
		t.Errorf("冷却为 0 时持续触发应仅通知一次: %d", n)
	}

	e.apply(rule, false, 0, now.Add(2*time.Minute)) // 恢复
	e.apply(rule, true, 8, now.Add(3*time.Minute))  // 再次触发
	e.mu.Lock()
	n = len(e.hist)
	last := e.hist[len(e.hist)-1]
	e.mu.Unlock()
	if n != 3 || last.Status != StatusFiring {
		t.Errorf("恢复后再次触发应重新告警: n=%d last=%+v", n, last)
	}
}

func TestPersistReload(t *testing.T) {
	dir := t.TempDir()
	e, err := New(nil, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("创建引擎失败: %v", err)
	}
	if _, err := e.SaveRule(Rule{Type: TypeTrafficThreshold, Threshold: 1024, Enabled: true}); err != nil {
		t.Fatalf("保存规则失败: %v", err)
	}
	if err := e.SaveWebhook(Webhook{Enabled: true, URL: "https://example.com/h", Format: "generic"}); err != nil {
		t.Fatalf("保存 Webhook 失败: %v", err)
	}

	e2, err := New(nil, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("重新加载引擎失败: %v", err)
	}
	if len(e2.Rules()) != 1 || e2.Webhook().URL != "https://example.com/h" {
		t.Errorf("持久化恢复不符: rules=%d webhook=%+v", len(e2.Rules()), e2.Webhook())
	}
}

// TestNewWithCorruptConfig alerts.json 损坏时不应阻断启动，降级为空配置
func TestNewWithCorruptConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "alerts.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0600); err != nil {
		t.Fatalf("写入损坏配置失败: %v", err)
	}
	e, err := New(nil, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("损坏配置不应导致创建失败: %v", err)
	}
	if len(e.Rules()) != 0 {
		t.Errorf("损坏配置应降级为空规则: %d", len(e.Rules()))
	}
	// 降级后仍可正常保存并持久化
	if _, err := e.SaveRule(Rule{Type: TypeConnThreshold, Threshold: 10, Enabled: true}); err != nil {
		t.Fatalf("降级后保存规则失败: %v", err)
	}
	if len(e.Rules()) != 1 {
		t.Errorf("保存后规则数 = %d, want 1", len(e.Rules()))
	}
}

// TestTunnelDownDeletedTunnel 删除隧道后应清除残留状态并解除告警，不再重复触发
func TestTunnelDownDeletedTunnel(t *testing.T) {
	mgr := tunnel.NewManager(logger.NewTest()) // 无任何隧道：Stats() 为空，视为已删除
	e, err := New(mgr, t.TempDir(), nil, nil, nil)
	if err != nil {
		t.Fatalf("创建引擎失败: %v", err)
	}

	rule := Rule{ID: "r1", Name: "断开告警", Type: TypeTunnelDown, TunnelID: "gone", Enabled: true, Duration: 30}
	e.mu.Lock()
	e.rules = []Rule{rule}
	// 模拟隧道删除后残留的 error 状态与未恢复的 firing
	e.errorSince["gone"] = time.Now().Add(-time.Hour)
	e.states["r1"] = &ruleState{firing: true, since: time.Now().Add(-time.Hour), lastFire: time.Now().Add(-time.Hour)}
	e.mu.Unlock()

	e.Evaluate()

	e.mu.Lock()
	_, hasErr := e.errorSince["gone"]
	st := e.states["r1"]
	hist := append([]History{}, e.hist...)
	e.mu.Unlock()

	if hasErr {
		t.Error("已删除隧道的 errorSince 应被清除")
	}
	if st == nil || st.firing {
		t.Error("firing 状态应解除")
	}
	if len(hist) != 1 || hist[0].Status != StatusResolved {
		t.Errorf("应产生一条 resolved 记录: %+v", hist)
	}

	// 再次评估不应产生新告警
	e.Evaluate()
	e.mu.Lock()
	n := len(e.hist)
	e.mu.Unlock()
	if n != 1 {
		t.Errorf("重复评估不应新增记录: %d", n)
	}
}
