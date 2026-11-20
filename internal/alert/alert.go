package alert

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
	"sshtunnel/internal/metrics"
	"sshtunnel/internal/tunnel"
)

// 规则类型与状态
const (
	TypeTunnelDown       = "tunnel_down"
	TypeConnThreshold    = "conn_threshold"
	TypeTrafficThreshold = "traffic_threshold"

	StatusFiring   = "firing"
	StatusResolved = "resolved"

	historyCapacity  = 200
	notifyQueueSize  = 64
	evaluateInterval = 10 * time.Second
	trafficWindow    = 5 * time.Minute
)

// Rule 告警规则
type Rule struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Enabled   bool      `json:"enabled"`
	TunnelID  string    `json:"tunnel_id,omitempty"`
	Threshold int64     `json:"threshold"`
	Duration  int       `json:"duration_seconds"`
	Cooldown  int       `json:"cooldown_seconds"`
	CreatedAt time.Time `json:"created_at"`
}

// Webhook 通知配置
type Webhook struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	Format  string `json:"format"` // generic | wecom | feishu
}

// History 一条告警历史
type History struct {
	Time       time.Time `json:"time"`
	RuleID     string    `json:"rule_id"`
	RuleName   string    `json:"rule_name"`
	TunnelID   string    `json:"tunnel_id,omitempty"`
	TunnelName string    `json:"tunnel_name,omitempty"`
	Type       string    `json:"type"`
	Value      int64     `json:"value"`
	Status     string    `json:"status"`
	Message    string    `json:"message"`
}

type ruleState struct {
	since    time.Time
	firing   bool
	lastFire time.Time
}

// Engine 告警引擎
type Engine struct {
	mu          sync.Mutex
	ctx         context.Context
	cancel      context.CancelFunc
	mgr         *tunnel.Manager
	store       *fileStore
	log         *logger.Logger
	metrics     *metrics.Store
	nameOf      func(string) string
	http        *http.Client
	rules       []Rule
	webhook     Webhook
	hist        []History
	historySink func(History) // 持久化钩子（nil 表示不落盘）
	states      map[string]*ruleState
	errorSince  map[string]time.Time
	listeners   []func(History)
	notifyCh    chan Notification
}

// New 创建告警引擎并启动评估与通知循环
func New(mgr *tunnel.Manager, dir string, log *logger.Logger, mstore *metrics.Store, nameOf func(string) string) (*Engine, error) {
	fs, err := newFileStore(dir)
	if err != nil {
		return nil, err
	}
	data, err := fs.Load()
	if err != nil {
		// 告警为可选功能：配置损坏不应阻断应用启动，降级为空配置（下次保存时覆盖）
		if log != nil {
			log.Warnf("告警配置加载失败，已忽略并使用空配置: %v", err)
		}
		data = Data{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		ctx:        ctx,
		cancel:     cancel,
		mgr:        mgr,
		store:      fs,
		log:        log,
		metrics:    mstore,
		nameOf:     nameOf,
		http:       &http.Client{Timeout: 5 * time.Second},
		rules:      data.Rules,
		webhook:    data.Webhook,
		states:     make(map[string]*ruleState),
		errorSince: make(map[string]time.Time),
		notifyCh:   make(chan Notification, notifyQueueSize),
	}
	if mgr != nil {
		mgr.OnStatusChange(e.HandleStatus)
	}
	go e.notifyLoop()
	go e.run()
	return e, nil
}

// OnEvent 注册告警事件监听（用于 WebSocket 推送）
func (e *Engine) OnEvent(fn func(History)) {
	if fn == nil {
		return
	}
	e.mu.Lock()
	e.listeners = append(e.listeners, fn)
	e.mu.Unlock()
}

// HandleStatus 跟踪隧道状态，用于「隧道断开」规则
func (e *Engine) HandleStatus(tunnelID, status string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if status == string(config.StatusError) {
		if _, ok := e.errorSince[tunnelID]; !ok {
			e.errorSince[tunnelID] = time.Now()
		}
		return
	}
	delete(e.errorSince, tunnelID)
}

func (e *Engine) run() {
	ticker := time.NewTicker(evaluateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.Evaluate()
		}
	}
}

// Evaluate 执行一轮规则评估
func (e *Engine) Evaluate() {
	if e.mgr == nil {
		return
	}
	stats := e.mgr.Stats()
	now := time.Now()
	var globalActive int64
	for _, s := range stats {
		globalActive += s.ActiveConns
	}
	for _, r := range e.Rules() {
		if !r.Enabled {
			continue
		}
		switch r.Type {
		case TypeTunnelDown:
			e.mu.Lock()
			since, ok := e.errorSince[r.TunnelID]
			e.mu.Unlock()
			// 隧道已删除（不在运行与归档统计中）：清除残留状态并解除告警，
			// 避免 errorSince 残留导致永久重复告警
			if _, alive := stats[r.TunnelID]; !alive {
				e.mu.Lock()
				delete(e.errorSince, r.TunnelID)
				e.mu.Unlock()
				e.apply(r, false, 0, now)
				continue
			}
			trigger := ok && now.Sub(since) >= time.Duration(r.Duration)*time.Second
			e.apply(r, trigger, 0, now)
		case TypeConnThreshold:
			value := globalActive
			if r.TunnelID != "" {
				value = stats[r.TunnelID].ActiveConns
			}
			e.apply(r, value > r.Threshold, value, now)
		case TypeTrafficThreshold:
			key := r.TunnelID
			if key == "" {
				key = metrics.GlobalKey
			}
			var value int64
			if e.metrics != nil {
				for _, p := range e.metrics.Query(key, now.Add(-trafficWindow), 60) {
					value += p.SentDelta + p.RecvDelta
				}
			}
			e.apply(r, value > r.Threshold, value, now)
		}
	}
}

// apply 根据触发状态维护持续时间/冷却并发出告警
func (e *Engine) apply(r Rule, trigger bool, value int64, now time.Time) {
	e.mu.Lock()
	st := e.states[r.ID]
	if st == nil {
		st = &ruleState{}
		e.states[r.ID] = st
	}
	if !trigger {
		wasFiring := st.firing
		st.firing = false
		st.since = time.Time{}
		e.mu.Unlock()
		if wasFiring {
			e.fire(r, value, StatusResolved)
		}
		return
	}
	if st.since.IsZero() {
		st.since = now
	}
	if now.Sub(st.since) < time.Duration(r.Duration)*time.Second {
		e.mu.Unlock()
		return
	}
	if st.firing {
		cooldown := time.Duration(r.Cooldown) * time.Second
		// 冷却为 0：持续触发期间不重复通知，恢复后再次触发才会重新告警
		if cooldown <= 0 || now.Sub(st.lastFire) < cooldown {
			e.mu.Unlock()
			return
		}
	}
	st.firing = true
	st.lastFire = now
	e.mu.Unlock()
	e.fire(r, value, StatusFiring)
}

func (e *Engine) fire(r Rule, value int64, status string) {
	name := ""
	if r.TunnelID != "" && e.nameOf != nil {
		name = e.nameOf(r.TunnelID)
	}
	h := History{
		Time:       time.Now(),
		RuleID:     r.ID,
		RuleName:   r.Name,
		TunnelID:   r.TunnelID,
		TunnelName: name,
		Type:       r.Type,
		Value:      value,
		Status:     status,
		Message:    buildMessage(r, name, value, status),
	}
	e.mu.Lock()
	e.hist = append(e.hist, h)
	if len(e.hist) > historyCapacity {
		e.hist = e.hist[len(e.hist)-historyCapacity:]
	}
	listeners := append([]func(History){}, e.listeners...)
	sink := e.historySink
	e.mu.Unlock()
	for _, fn := range listeners {
		fn(h)
	}
	if sink != nil {
		sink(h)
	}
	select {
	case e.notifyCh <- Notification{History: h, Rule: r}:
	default:
		if e.log != nil {
			e.log.Warn("告警通知队列已满，丢弃本次通知")
		}
	}
}

func (e *Engine) notifyLoop() {
	for {
		select {
		case <-e.ctx.Done():
			return
		case n := <-e.notifyCh:
			e.mu.Lock()
			w := e.webhook
			e.mu.Unlock()
			if err := SendNotification(e.http, w, n); err != nil && e.log != nil {
				e.log.Warnf("告警 Webhook 发送失败: %v", err)
			}
		}
	}
}

// Close 停止评估与通知循环（进程退出前调用；调用后引擎不再产生告警）
func (e *Engine) Close() {
	if e.cancel != nil {
		e.cancel()
	}
}

// SetHistorySink 设置告警历史持久化钩子（每条触发/恢复记录调用一次）
func (e *Engine) SetHistorySink(fn func(History)) {
	e.mu.Lock()
	e.historySink = fn
	e.mu.Unlock()
}

// RestoreHistory 恢复告警历史（升序传入，保留最近 historyCapacity 条）
func (e *Engine) RestoreHistory(recs []History) {
	e.mu.Lock()
	e.hist = append(e.hist, recs...)
	if len(e.hist) > historyCapacity {
		e.hist = e.hist[len(e.hist)-historyCapacity:]
	}
	e.mu.Unlock()
}

// History 返回最近的告警历史（时间倒序）
func (e *Engine) History(limit int) []History {
	e.mu.Lock()
	defer e.mu.Unlock()
	if limit <= 0 || limit > len(e.hist) {
		limit = len(e.hist)
	}
	out := make([]History, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, e.hist[len(e.hist)-1-i])
	}
	return out
}

// Rules 返回规则副本
func (e *Engine) Rules() []Rule {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Rule{}, e.rules...)
}

// SaveRule 新增或更新规则
func (e *Engine) SaveRule(r Rule) (Rule, error) {
	r.Type = strings.TrimSpace(r.Type)
	r.Name = strings.TrimSpace(r.Name)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := validateRule(&r); err != nil {
		return Rule{}, err
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
		r.CreatedAt = time.Now()
		e.rules = append(e.rules, r)
	} else {
		found := false
		for i := range e.rules {
			if e.rules[i].ID == r.ID {
				r.CreatedAt = e.rules[i].CreatedAt
				e.rules[i] = r
				found = true
				break
			}
		}
		if !found {
			return Rule{}, fmt.Errorf("规则不存在")
		}
	}
	if err := e.persistLocked(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// DeleteRule 删除规则
func (e *Engine) DeleteRule(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := -1
	for i := range e.rules {
		if e.rules[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("规则不存在")
	}
	e.rules = append(e.rules[:idx], e.rules[idx+1:]...)
	delete(e.states, id)
	return e.persistLocked()
}

// Webhook 返回通知配置
func (e *Engine) Webhook() Webhook {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.webhook
}

// SaveWebhook 保存通知配置
func (e *Engine) SaveWebhook(w Webhook) error {
	w.URL = strings.TrimSpace(w.URL)
	if w.Enabled && !strings.HasPrefix(w.URL, "http://") && !strings.HasPrefix(w.URL, "https://") {
		return fmt.Errorf("Webhook 地址必须以 http:// 或 https:// 开头")
	}
	switch w.Format {
	case "", "generic", "wecom", "feishu":
	default:
		return fmt.Errorf("不支持的通知格式: %s", w.Format)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.webhook = w
	return e.persistLocked()
}

// TestWebhook 发送测试通知
func (e *Engine) TestWebhook() error {
	e.mu.Lock()
	w := e.webhook
	e.mu.Unlock()
	if !w.Enabled || w.URL == "" {
		return fmt.Errorf("Webhook 未启用或未配置地址")
	}
	now := time.Now()
	return SendNotification(e.http, w, Notification{
		Rule: Rule{ID: "test", Name: "测试通知", Type: "test"},
		History: History{
			Time:     now,
			RuleID:   "test",
			RuleName: "测试通知",
			Type:     "test",
			Status:   StatusFiring,
			Message:  "SSHTunnel 测试通知：" + now.Format("2006-01-02 15:04:05"),
		},
	})
}

func (e *Engine) persistLocked() error {
	err := e.store.Save(Data{Rules: append([]Rule{}, e.rules...), Webhook: e.webhook})
	if err != nil && e.log != nil {
		e.log.Errorf("告警配置保存失败: %v", err)
	}
	return err
}

func validateRule(r *Rule) error {
	switch r.Type {
	case TypeTunnelDown:
		if r.TunnelID == "" {
			return fmt.Errorf("隧道断开规则必须指定隧道")
		}
	case TypeConnThreshold, TypeTrafficThreshold:
		if r.Threshold <= 0 {
			return fmt.Errorf("阈值必须大于 0")
		}
	default:
		return fmt.Errorf("未知的规则类型: %s", r.Type)
	}
	if r.Name == "" {
		switch r.Type {
		case TypeTunnelDown:
			r.Name = "隧道断开"
		case TypeConnThreshold:
			r.Name = "连接数告警"
		default:
			r.Name = "流量告警"
		}
	}
	if r.Duration < 0 {
		r.Duration = 0
	}
	if r.Cooldown < 0 {
		r.Cooldown = 0
	}
	return nil
}

func statusText(status string) string {
	if status == StatusResolved {
		return "恢复"
	}
	return "告警"
}

func buildMessage(r Rule, tunnelName string, value int64, status string) string {
	scope := "全局"
	if tunnelName != "" {
		scope = tunnelName
	} else if r.TunnelID != "" {
		scope = r.TunnelID
	}
	switch r.Type {
	case TypeTunnelDown:
		if status == StatusFiring {
			return fmt.Sprintf("【SSHTunnel 告警】隧道 %s 连接异常断开", scope)
		}
		return fmt.Sprintf("【SSHTunnel 恢复】隧道 %s 连接已恢复", scope)
	case TypeConnThreshold:
		return fmt.Sprintf("【SSHTunnel %s】%s 活动连接数 %d（阈值 %d）", statusText(status), scope, value, r.Threshold)
	case TypeTrafficThreshold:
		return fmt.Sprintf("【SSHTunnel %s】%s 5 分钟流量 %s（阈值 %s）", statusText(status), scope, formatBytes(value), formatBytes(r.Threshold))
	default:
		return fmt.Sprintf("【SSHTunnel %s】规则 %s 触发", statusText(status), r.Name)
	}
}

func formatBytes(n int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
	)
	switch {
	case n >= gb:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gb))
	case n >= mb:
		return fmt.Sprintf("%.2f MB", float64(n)/float64(mb))
	case n >= kb:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kb))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
