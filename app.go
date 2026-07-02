package main

import (
	"context"
	"fmt"

	"sshtunnel/internal/config"
	"sshtunnel/internal/logger"
	"sshtunnel/internal/port"
	"sshtunnel/internal/proxy"
	"sshtunnel/internal/tunnel"
)

// App Wails 应用绑定层
type App struct {
	ctx       context.Context
	logger    *logger.Logger
	cfgMgr    *config.Manager
	tunnelMgr *tunnel.Manager
}

// NewApp 创建应用实例
func NewApp() *App {
	return &App{}
}

// Init 初始化各模块
func (a *App) Init() error {
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

	a.logger.Info("SSH Tunnel Manager 启动完成")
	return nil
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

// ==================== 系统服务 ====================

// CheckPort 检查端口是否可用
func (a *App) CheckPort(portNum int) bool {
	return port.IsAvailable(portNum)
}

// GetRecentLogs 获取最近日志
func (a *App) GetRecentLogs(limit int) []logger.Entry {
	return a.logger.Recent(limit)
}

// GenerateSelfSignedCert 一键生成 HTTPS 代理自签证书
func (a *App) GenerateSelfSignedCert() (certPath, keyPath string, err error) {
	return proxy.GenerateSelfSignedCert()
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

// 用于上下文传递（保留 context 引用）
var _ = context.Background
