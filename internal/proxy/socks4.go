package proxy

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"

	"sshtunnel/internal/config"
)

// socks4Server SOCKS4/4a 代理服务器
type socks4Server struct {
	*BaseServer
	auth   *config.AuthConfig
	helper authHelper
}

func newSOCKS4Server(base *BaseServer, auth *config.AuthConfig) *socks4Server {
	return &socks4Server{
		BaseServer: base,
		auth:       auth,
		helper:     authHelper{auth: auth},
	}
}

func (s *socks4Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		s.setStatus(StatusListenerError)
		s.log("error", "监听失败: "+err.Error())
		return err
	}
	s.mu.Lock()
	s.listener = ln
	s.status = StatusListenerRunning
	s.mu.Unlock()
	s.log("info", "SOCKS4 代理已启动")

	go func() {
		select {
		case <-ctx.Done():
			s.Stop()
		case <-s.stopChan():
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			running := s.status == StatusListenerRunning
			s.mu.Unlock()
			if !running {
				return nil
			}
			s.log("error", "接受连接失败: "+err.Error())
			time.Sleep(100 * time.Millisecond) // 持续错误（如 EMFILE）时退避，避免忙等
			continue
		}
		go s.handleConn(conn)
	}
}

func (s *socks4Server) Stop() error {
	s.setStatus(StatusListenerStopped)
	return s.stop()
}

func (s *socks4Server) handleConn(conn net.Conn) {
	defer conn.Close()
	s.stats.BeginConn()
	defer s.stats.EndConn()

	// 简易防火墙：来源校验
	if !s.allowConn(conn) {
		return
	}

	// 握手阶段限制读超时，防止慢速连接长期占用 goroutine
	_ = conn.SetReadDeadline(time.Now().Add(s.hsTimeout))
	if tc, ok := conn.(*tls.Conn); ok {
		ctx, cancel := context.WithTimeout(context.Background(), s.hsTimeout)
		err := tc.HandshakeContext(ctx)
		cancel()
		if err != nil {
			return
		}
	}

	start := time.Now()
	// SOCKS4 协议: VN(1) CD(1) DSTPORT(2) DSTIP(4) USERID(null-terminated)
	header := make([]byte, 8)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}
	if header[0] != 0x04 {
		return
	}
	cmd := header[1]
	port := binary.BigEndian.Uint16(header[2:4])
	ip := net.IPv4(header[4], header[5], header[6], header[7])

	// 读取 USERID (null-terminated)
	userID, err := readNullString(conn)
	if err != nil {
		return
	}

	// 仅支持 CONNECT
	if cmd != 0x01 {
		s.reply(conn, 0x5B) // 请求拒绝或失败
		return
	}

	var host string
	// SOCKS4a: IP 为 0.0.0.x (x!=0) 时，USERID 后跟域名 (null-terminated)
	if header[4] == 0 && header[5] == 0 && header[6] == 0 && header[7] != 0 {
		domain, err := readNullString(conn)
		if err != nil {
			return
		}
		host = domain
	} else {
		host = ip.String()
	}

	// 认证：检查 UserID
	if !s.helper.checkUserID(userID) {
		s.reply(conn, 0x5D) // 用户名不匹配
		s.log("warn", "SOCKS4 认证失败: "+sanitizeLog(userID))
		s.logAccessDenied(conn, "", "认证失败")
		return
	}

	target := joinTarget(host, int(port))

	// 握手完成，清除读超时（后续桥接可能长时间空闲）
	_ = conn.SetReadDeadline(time.Time{})

	// 通过 SSH 隧道连接目标
	targetConn, err := s.dialer.Dial("tcp", target)
	if err != nil {
		s.log("warn", "连接目标失败 "+target+": "+err.Error())
		s.logAccess(conn, target, start, "失败: 连接目标失败")
		s.reply(conn, 0x5B) // 失败
		return
	}
	defer targetConn.Close()

	// 目标已连通：登记为活动连接（桥接结束时注销）
	entry := s.trackConn(conn, target)
	defer s.closeConn(entry)

	// 回复成功
	s.reply(conn, 0x5A)

	// 双向桥接
	BridgeStats(conn, targetConn, s.stats, entry)
	s.logAccess(conn, target, start, "成功")
}

// reply 发送 SOCKS4 响应: VN(0x00) CD(1)
func (s *socks4Server) reply(conn net.Conn, code byte) {
	// 响应前 6 字节为填充（端口与 IP，客户端通常忽略）
	resp := []byte{0x00, code, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	conn.Write(resp)
}

// maxSocksFieldLen SOCKS4 USERID/域名字段最大长度（防止未认证的超长输入耗尽内存）
const maxSocksFieldLen = 1024

// readNullString 读取以 null 结尾的字符串
func readNullString(r io.Reader) (string, error) {
	var buf []byte
	one := make([]byte, 1)
	for {
		if _, err := io.ReadFull(r, one); err != nil {
			return "", err
		}
		if one[0] == 0 {
			break
		}
		if len(buf) >= maxSocksFieldLen {
			return "", fmt.Errorf("字段长度超过 %d 字节", maxSocksFieldLen)
		}
		buf = append(buf, one[0])
	}
	return string(buf), nil
}

// 确保 socks4Server 实现 Server 接口
var _ Server = (*socks4Server)(nil)

// 防止未使用 config
var _ = config.ProxyHTTP
