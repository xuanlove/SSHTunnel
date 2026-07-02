package proxy

import (
	"context"
	"fmt"
	"net"
	"sync"

	"sshsuidao/internal/config"
	"sshsuidao/internal/port"
)

// Dialer 出站 Dialer 抽象，由 SSH 隧道提供
type Dialer interface {
	Dial(network, address string) (net.Conn, error)
}

// ListenerStatus 监听器运行状态
type ListenerStatus string

const (
	StatusListenerStopped  ListenerStatus = "stopped"
	StatusListenerRunning  ListenerStatus = "running"
	StatusListenerError    ListenerStatus = "error"
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
	base := BaseServer{
		id:         lc.ID,
		protocol:   lc.Protocol,
		listenAddr: addr,
		dialer:     dialer,
		log:        log,
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
