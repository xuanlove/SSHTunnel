package alert

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
