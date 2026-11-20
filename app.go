package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"sshtunnel/internal/alert"
	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
	"sshtunnel/internal/metrics"
	"sshtunnel/internal/port"
	"sshtunnel/internal/proxy"
	"sshtunnel/internal/tunnel"
)

// App Wails 应用绑定层
type App struct {
	ctx       context.Context
	initOnce  sync.Once
	initErr   error
	logger    *logger.Logger
	cfgMgr    *config.Manager
	tunnelMgr *tunnel.Manager
	metrics   *metrics.Store
	alerts    *alert.Engine
	version   string
	runCtx    context.Context    // 后台采样上下文
	runStop   context.CancelFunc // 停止后台采样
}

// NewApp 创建应用实例
func NewApp() *App {
	return &App{}
}

// SetVersion 注入构建版本号（桌面端节点状态展示用）
func (a *App) SetVersion(v string) {
	a.version = v
}

// GetVersion 返回构建版本号（设置页展示用）
func (a *App) GetVersion() string {
	return a.version
}

// Init 初始化各模块（幂等：both 模式下 WEB 启动与 Wails startup 均会调用）
func (a *App) Init() error {
	a.initOnce.Do(func() { a.initErr = a.init() })
	return a.initErr
}

func (a *App) init() error {
	log, err := logger.New()
	if err != nil {
		return fmt.Errorf("初始化日志失败: %w", err)
	}
	a.logger = log

	cfgMgr, err := config.NewManager()
	if err != nil {
		return fmt.Errorf("初始化配置管理失败: %w", err)
	}
	a.cfgMgr = cfgMgr

	a.tunnelMgr = tunnel.NewManager(a.logger)

	// 隧道状态变更同步到配置状态：确保异常终止（如重连次数耗尽）后，
	// 界面查询到的状态与实际运行情况一致。
	a.tunnelMgr.OnStatusChange(func(tunnelID, status string) {
		a.cfgMgr.SetStatus(tunnelID, config.TunnelStatus(status))
	})

	// 指标采样（24h 内存环形）与告警引擎。
	// 须在自启隧道之前初始化：隧道启动失败的 error 状态才能被告警引擎捕获。
	a.runCtx, a.runStop = context.WithCancel(context.Background())
	a.metrics = metrics.NewStore(24*time.Hour, 10*time.Second)
	go a.metrics.Run(a.runCtx, a.snapshotMetrics)
	alertDir, err := config.Dir()
	if err != nil {
		return fmt.Errorf("初始化告警目录失败: %w", err)
	}
	a.alerts, err = alert.New(a.tunnelMgr, alertDir, a.logger, a.metrics, a.tunnelName)
	if err != nil {
		return fmt.Errorf("初始化告警引擎失败: %w", err)
	}

	// 启动已勾选「开机启动」的隧道（异步执行，避免阻塞界面/服务启动）
	go a.startAutoStartTunnels()

	a.logger.Info("SSH Tunnel Manager 启动完成")
	return nil
}

// Close 停止后台采样与告警循环（进程退出前调用）
func (a *App) Close() {
	if a.runStop != nil {
		a.runStop()
	}
	if a.alerts != nil {
		a.alerts.Close()
	}
}

// snapshotMetrics 汇总当前统计供采样
func (a *App) snapshotMetrics() map[string]metrics.Raw {
	out := make(map[string]metrics.Raw)
	var g metrics.Raw
	for id, st := range a.tunnelMgr.Stats() {
		out[id] = metrics.Raw{
			ActiveConns: st.ActiveConns,
			TotalConns:  st.TotalConns,
			BytesSent:   st.BytesSent,
			BytesRecv:   st.BytesRecv,
		}
		g.ActiveConns += st.ActiveConns
		g.TotalConns += st.TotalConns
		g.BytesSent += st.BytesSent
		g.BytesRecv += st.BytesRecv
	}
	out[metrics.GlobalKey] = g
	return out
}

// tunnelName 按 ID 查找隧道名称
func (a *App) tunnelName(id string) string {
	for _, c := range a.cfgMgr.List() {
		if c.ID == id {
			return c.Name
		}
	}
	return ""
}

// GetMetrics 返回趋势数据（rangeKey: 1h/6h/24h；tunnelID 为空表示全局）
func (a *App) GetMetrics(rangeKey string, tunnelID string) []metrics.Point {
	if a.metrics == nil {
		return []metrics.Point{}
	}
	key := tunnelID
	if key == "" {
		key = metrics.GlobalKey
	}
	return a.metrics.Query(key, time.Now().Add(-metrics.ParseRange(rangeKey)), 120)
}

// GetAlertRules 返回告警规则
func (a *App) GetAlertRules() []alert.Rule {
	if a.alerts == nil {
		return []alert.Rule{}
	}
	return a.alerts.Rules()
}

// SaveAlertRule 新增或更新告警规则
func (a *App) SaveAlertRule(r alert.Rule) (alert.Rule, error) {
	if a.alerts == nil {
		return alert.Rule{}, fmt.Errorf("告警引擎未初始化")
	}
	return a.alerts.SaveRule(r)
}

// DeleteAlertRule 删除告警规则
func (a *App) DeleteAlertRule(id string) error {
	if a.alerts == nil {
		return fmt.Errorf("告警引擎未初始化")
	}
	return a.alerts.DeleteRule(id)
}

// GetAlertHistory 返回最近的告警历史
func (a *App) GetAlertHistory(limit int) []alert.History {
	if a.alerts == nil {
		return []alert.History{}
	}
	return a.alerts.History(limit)
}

// GetAlertWebhook 返回通知配置
func (a *App) GetAlertWebhook() alert.Webhook {
	if a.alerts == nil {
		return alert.Webhook{}
	}
	return a.alerts.Webhook()
}

// SaveAlertWebhook 保存通知配置（返回规范化后的配置）
func (a *App) SaveAlertWebhook(w alert.Webhook) (alert.Webhook, error) {
	if a.alerts == nil {
		return alert.Webhook{}, fmt.Errorf("告警引擎未初始化")
	}
	if err := a.alerts.SaveWebhook(w); err != nil {
		return alert.Webhook{}, err
	}
	return a.alerts.Webhook(), nil
}

// TestAlertWebhook 发送测试通知
func (a *App) TestAlertWebhook() error {
	if a.alerts == nil {
		return fmt.Errorf("告警引擎未初始化")
	}
	return a.alerts.TestWebhook()
}

// startAutoStartTunnels 启动所有标记为开机启动的隧道
func (a *App) startAutoStartTunnels() {
	for _, cfg := range a.cfgMgr.List() {
		if !cfg.AutoStart {
			continue
		}
		c := cfg
		a.logger.Infof("开机启动隧道: %s (ID: %s)", c.Name, c.ID)
		a.cfgMgr.SetStatus(c.ID, config.StatusStarting)
		if err := a.tunnelMgr.Start(&c); err != nil {
			a.cfgMgr.SetStatus(c.ID, config.StatusError)
			a.logger.Errorf("开机启动失败: %s - %v", c.Name, err)
			continue
		}
		a.cfgMgr.SetStatus(c.ID, config.StatusRunning)
		a.logger.Infof("开机启动成功: %s (ID: %s)", c.Name, c.ID)
	}
}

// ==================== 配置相关 ====================

// ListConfigs 返回所有隧道配置
func (a *App) ListConfigs() []config.TunnelConfig {
	return a.cfgMgr.List()
}

// SaveConfig 保存（新建或更新）隧道配置
func (a *App) SaveConfig(cfg config.TunnelConfig) (config.TunnelConfig, error) {
	if err := a.cfgMgr.Save(&cfg); err != nil {
		return cfg, err
	}
	a.logger.Infof("配置已保存: %s (ID: %s)", cfg.Name, cfg.ID)
	return cfg, nil
}

// DeleteConfig 删除隧道配置
func (a *App) DeleteConfig(id string) error {
	if a.tunnelMgr.IsRunning(id) {
		return fmt.Errorf("隧道运行中，请先停止")
	}
	if err := a.cfgMgr.Delete(id); err != nil {
		return err
	}
	// 清理该隧道的历史累计与残留连接
	a.tunnelMgr.Forget(id)
	a.logger.Infof("配置已删除: ID: %s", id)
	return nil
}

// ==================== 隧道控制 ====================

// StartTunnel 启动指定隧道
func (a *App) StartTunnel(id string) error {
	cfg, ok := a.cfgMgr.Get(id)
	if !ok {
		return fmt.Errorf("配置不存在: %s", id)
	}
	a.cfgMgr.SetStatus(id, config.StatusStarting)
	a.logger.Infof("正在启动隧道: %s (ID: %s)", cfg.Name, id)
	if err := a.tunnelMgr.Start(cfg); err != nil {
		a.cfgMgr.SetStatus(id, config.StatusError)
		a.logger.Errorf("隧道启动失败: %s - %v", cfg.Name, err)
		return err
	}
	a.cfgMgr.SetStatus(id, config.StatusRunning)
	a.logger.Infof("隧道已启动: %s (ID: %s)", cfg.Name, id)
	return nil
}

// StopTunnel 停止指定隧道
func (a *App) StopTunnel(id string) error {
	if err := a.tunnelMgr.Stop(id); err != nil {
		return err
	}
	a.cfgMgr.SetStatus(id, config.StatusStopped)
	a.logger.Infof("隧道已停止: ID: %s", id)
	return nil
}

// StartAll 启动所有配置的隧道
func (a *App) StartAll() error {
	a.logger.Info("批量启动所有隧道")
	for _, cfg := range a.cfgMgr.List() {
		if !a.tunnelMgr.IsRunning(cfg.ID) {
			if err := a.tunnelMgr.Start(&cfg); err != nil {
				a.cfgMgr.SetStatus(cfg.ID, config.StatusError)
				a.logger.Errorf("批量启动失败: %s - %v", cfg.Name, err)
				return fmt.Errorf("启动 %s 失败: %w", cfg.Name, err)
			}
			a.cfgMgr.SetStatus(cfg.ID, config.StatusRunning)
		}
	}
	return nil
}

// StopAll 停止所有隧道
func (a *App) StopAll() error {
	a.logger.Info("批量停止所有隧道")
	a.tunnelMgr.StopAll()
	for _, cfg := range a.cfgMgr.List() {
		a.cfgMgr.SetStatus(cfg.ID, config.StatusStopped)
	}
	return nil
}

// GetTunnelStats 返回所有隧道的连接与流量统计（桌面端，含已停止隧道的累计值）
func (a *App) GetTunnelStats() map[string]tunnel.TunnelStats {
	if a.tunnelMgr == nil {
		return map[string]tunnel.TunnelStats{}
	}
	return a.tunnelMgr.Stats()
}

// GetNodeStatus 返回本节点运行状态：概览、活动连接、流量与访问历史（桌面端）
func (a *App) GetNodeStatus(historyLimit int) tunnel.NodeStatus {
	if a.tunnelMgr == nil || a.cfgMgr == nil {
		return tunnel.NodeStatus{
			Connections:   []proxy.ConnSnapshot{},
			AccessHistory: []proxy.AccessRecord{},
			Tunnels:       []tunnel.TunnelTraffic{},
			Version:       a.version,
		}
	}
	st := a.tunnelMgr.NodeStatus(a.cfgMgr.List(), historyLimit)
	st.Version = a.version
	return st
}

// GetNodeHistory 返回指定隧道最近的访问历史（节点详情页使用）
func (a *App) GetNodeHistory(tunnelID string, limit int) []proxy.AccessRecord {
	if a.tunnelMgr == nil {
		return []proxy.AccessRecord{}
	}
	return a.tunnelMgr.NodeHistory(tunnelID, limit)
}

// ==================== 系统服务 ====================

// CheckPort 检查端口是否可用
func (a *App) CheckPort(portNum int) bool {
	return port.IsAvailable(portNum)
}

// GetRecentLogs 获取最近日志
func (a *App) GetRecentLogs(limit int) []logger.Entry {
	return a.logger.Recent(limit)
}

// GenerateSelfSignedCert 一键生成 HTTPS 代理自签证书（返回 PEM 文本）
func (a *App) GenerateSelfSignedCert() (certPEM, keyPEM string, err error) {
	return proxy.GenerateSelfSignedPEM()
}

// InspectCertificate 解析证书 PEM，返回使用者公用名与有效期（桌面端）
func (a *App) InspectCertificate(certPEM string) (proxy.CertInfo, error) {
	info, err := proxy.InspectCertificate(certPEM)
	if err != nil {
		return proxy.CertInfo{}, err
	}
	return *info, nil
}

// ParseHopChainString 解析简写格式跳板链字符串
func (a *App) ParseHopChainString(s string) []config.HopConfig {
	hops := parseSimpleHopChain(s)
	out := make([]config.HopConfig, len(hops))
	for i, h := range hops {
		out[i] = config.HopConfig{
			User: h.User,
			Host: h.Host,
			Port: h.Port,
		}
	}
	return out
}

// TestTunnelConfig 测试隧道连接（不启动监听）
func (a *App) TestTunnelConfig(cfg config.TunnelConfig) error {
	// 通过临时 SSH 拨号测试连通性
	hops := make([]simpleHop, len(cfg.HopChain))
	for i, h := range cfg.HopChain {
		hops[i] = simpleHop{
			User:       h.User,
			Host:       h.Host,
			Port:       h.Port,
			AuthType:   h.AuthType,
			Password:   h.Password,
			KeyContent: h.KeyContent,
			Passphrase: h.Passphrase,
		}
	}
	return testSSHConnection(hops)
}

// TestProxyListener 测试代理监听端口连通性（经代理访问目标，返回出口 IP）
func (a *App) TestProxyListener(protocol string, portNum int, username, password string) (*proxy.ProbeResult, error) {
	return proxy.Probe(proxy.ProbeOptions{
		Protocol: protocol,
		Host:     "127.0.0.1",
		Port:     portNum,
		Username: username,
		Password: password,
	})
}

// GeoIPAvailable 是否已加载 GeoIP 数据库（代理防火墙国家规则依赖它）
func (a *App) GeoIPAvailable() bool {
	return proxy.GeoIPAvailable()
}

// GeoIPStatus 返回当前 GeoIP 能力状态（桌面端）
func (a *App) GeoIPStatus() proxy.GeoIPStatus {
	return proxy.CurrentGeoIPStatus()
}

// CityCatalog 返回指定国家的省/城市下拉选项（桌面端）
func (a *App) CityCatalog(country string) proxy.CityCatalogResponse {
	return proxy.CityCatalog(country)
}

// DownloadGeoIPDatabase 下载/更新城市级数据库（桌面端）
func (a *App) DownloadGeoIPDatabase(url string) (proxy.GeoIPStatus, error) {
	if _, _, err := proxy.DownloadCityDatabase(url); err != nil {
		return proxy.CurrentGeoIPStatus(), err
	}
	return proxy.CurrentGeoIPStatus(), nil
}

// SetGeoIPEnabled 启用/关闭城市级数据库（桌面端，热加载）
func (a *App) SetGeoIPEnabled(enabled bool) (proxy.GeoIPStatus, error) {
	if enabled {
		if err := proxy.LoadGeoIPDatabaseFile(proxy.CityDatabasePath()); err != nil {
			return proxy.CurrentGeoIPStatus(), err
		}
	} else if err := proxy.LoadBuiltinGeoDatabase(); err != nil {
		return proxy.CurrentGeoIPStatus(), err
	}
	if err := proxy.SaveGeoIPSettings(proxy.GeoIPSettings{Enabled: enabled}); err != nil {
		return proxy.CurrentGeoIPStatus(), err
	}
	return proxy.CurrentGeoIPStatus(), nil
}

// 用于上下文传递（保留 context 引用）
var _ = context.Background
