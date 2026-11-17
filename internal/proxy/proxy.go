package proxy

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"sshtunnel/internal/config"
	"sshtunnel/internal/port"
)

// Dialer 出站 Dialer 抽象，由 SSH 隧道提供
type Dialer interface {
	Dial(network, address string) (net.Conn, error)
}

// ListenerStatus 监听器运行状态
type ListenerStatus string

const (
	StatusListenerStopped ListenerStatus = "stopped"
	StatusListenerRunning ListenerStatus = "running"
	StatusListenerError   ListenerStatus = "error"
)

// Server 代理服务器统一接口
type Server interface {
	// Start 启动监听（阻塞，需 goroutine 调用）
	Start(ctx context.Context) error
	// Stop 停止监听
	Stop() error
	// Status 当前状态
	Status() ListenerStatus
	// Protocol 协议类型
	Protocol() config.ProxyProtocol
	// ListenAddr 监听地址
	ListenAddr() string
	// ID 监听器 ID
	ID() string
}

// BaseServer 公共基础结构
type BaseServer struct {
	mu         sync.Mutex
	id         string
	protocol   config.ProxyProtocol
	listenAddr string
	status     ListenerStatus
	listener   net.Listener
	dialer     Dialer
	log        func(level string, msg string)
	fw         *Firewall // 简易防火墙（nil 表示不限制）

	// 防火墙拦截日志去重：同一来源 IP 在窗口期内只记录一次
	blockLogMu sync.Mutex
	blockLogAt map[string]time.Time
}

// firewallBlockLogInterval 同一来源 IP 的防火墙拦截日志最小间隔
const firewallBlockLogInterval = 10 * time.Minute

// shouldLogBlock 判断该来源的拦截日志是否应记录（同一 IP 十分钟内仅一次）
func (b *BaseServer) shouldLogBlock(src string) bool {
	now := time.Now()
	b.blockLogMu.Lock()
	defer b.blockLogMu.Unlock()
	if b.blockLogAt == nil {
		b.blockLogAt = make(map[string]time.Time)
	}
	if last, ok := b.blockLogAt[src]; ok && now.Sub(last) < firewallBlockLogInterval {
		return false
	}
	// 顺带清理过期条目，避免长期运行内存增长
	for k, t := range b.blockLogAt {
		if now.Sub(t) >= firewallBlockLogInterval {
			delete(b.blockLogAt, k)
		}
	}
	b.blockLogAt[src] = now
	return true
}

// allowConn 依据防火墙规则判断是否允许该连接接入。
// 各协议在 handleConn 开头调用；拒绝时记录告警日志（同一来源 IP 十分钟内只记一次）。
func (b *BaseServer) allowConn(conn net.Conn) bool {
	if b.fw == nil {
		return true
	}
	ip := remoteIP(conn)
	if b.fw.Allow(ip) {
		return true
	}
	src := "unknown"
	if ip != nil {
		src = ip.String()
	}
	if b.log != nil && b.shouldLogBlock(src) {
		b.log("warn", "防火墙已拒绝来源 "+src+"（同一来源 10 分钟内仅提示一次）")
	}
	return false
}

func (b *BaseServer) Status() ListenerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

func (b *BaseServer) Protocol() config.ProxyProtocol { return b.protocol }
func (b *BaseServer) ListenAddr() string             { return b.listenAddr }
func (b *BaseServer) ID() string                     { return b.id }

func (b *BaseServer) setStatus(s ListenerStatus) {
	b.mu.Lock()
	b.status = s
	b.mu.Unlock()
}

func (b *BaseServer) stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.listener != nil {
		err := b.listener.Close()
		b.listener = nil
		return err
	}
	return nil
}

// LogFunc 日志回调类型
type LogFunc func(level string, msg string)

// noopLog 空日志
func noopLog(level, msg string) {}

// BuildServer 根据配置构建对应协议的服务器
func BuildServer(lc config.ProxyListener, dialer Dialer, log LogFunc) (Server, error) {
	if log == nil {
		log = noopLog
	}
	addr := port.BindAddress(lc.ListenPort, lc.AllowExternal)

	fw, err := NewFirewall(lc.Firewall, defaultGeoResolver{})
	if err != nil {
		return nil, fmt.Errorf("防火墙配置错误: %w", err)
	}

	base := BaseServer{
		id:         lc.ID,
		protocol:   lc.Protocol,
		listenAddr: addr,
		dialer:     dialer,
		log:        log,
		fw:         fw,
	}

	switch lc.Protocol {
	case config.ProxyHTTP:
		return newHTTPServer(base, lc.Auth, nil), nil
	case config.ProxyHTTPS:
		if lc.TLS == nil {
			return nil, fmt.Errorf("HTTPS 代理需要 TLS 证书配置")
		}
		return newHTTPServer(base, lc.Auth, lc.TLS), nil
	case config.ProxySOCKS4:
		return newSOCKS4Server(base, lc.Auth), nil
	case config.ProxySOCKS5:
		return newSOCKS5Server(base, lc.Auth), nil
	default:
		return nil, fmt.Errorf("不支持的协议: %s", lc.Protocol)
	}
}

// checkPortConflict 检查端口冲突
func checkPortConflict(listeners []config.ProxyListener) error {
	seen := make(map[int]string)
	for _, lc := range listeners {
		if prev, ok := seen[lc.ListenPort]; ok {
			return fmt.Errorf("端口 %d 被 %s 与 %s 同时占用", lc.ListenPort, prev, lc.ID)
		}
		seen[lc.ListenPort] = lc.ID
	}
	return nil
}
