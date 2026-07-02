package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"

	"sshtunnel/internal/config"
)

// socks4Server SOCKS4/4a 代理服务器
type socks4Server struct {
	BaseServer
	auth   *config.AuthConfig
	helper authHelper
}

func newSOCKS4Server(base BaseServer, auth *config.AuthConfig) *socks4Server {
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
		<-ctx.Done()
		s.Stop()
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
		s.log("warn", "SOCKS4 认证失败: "+userID)
		return
	}

	target := net.JoinHostPort(host, strconv.Itoa(int(port)))

	// 通过 SSH 隧道连接目标
	targetConn, err := s.dialer.Dial("tcp", target)
	if err != nil {
		s.log("warn", "连接目标失败 "+target+": "+err.Error())
		s.reply(conn, 0x5B) // 失败
		return
	}
	defer targetConn.Close()

	// 回复成功
	s.reply(conn, 0x5A)

	// 双向桥接
	Bridge(conn, targetConn)
}

// reply 发送 SOCKS4 响应: VN(0x00) CD(1)
func (s *socks4Server) reply(conn net.Conn, code byte) {
	// 响应前 6 字节为填充（端口与 IP，客户端通常忽略）
	resp := []byte{0x00, code, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	conn.Write(resp)
}

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
		buf = append(buf, one[0])
	}
	return string(buf), nil
}

// 确保 socks4Server 实现 Server 接口
var _ Server = (*socks4Server)(nil)

// 防止未使用 config
var _ = config.ProxyHTTP
