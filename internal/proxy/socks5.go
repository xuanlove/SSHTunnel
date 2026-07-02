package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"

	"sshtunnel/internal/config"
)

// socks5Server SOCKS5 代理服务器
type socks5Server struct {
	BaseServer
	auth   *config.AuthConfig
	helper authHelper
}

func newSOCKS5Server(base BaseServer, auth *config.AuthConfig) *socks5Server {
	return &socks5Server{
		BaseServer: base,
		auth:       auth,
		helper:     authHelper{auth: auth},
	}
}

func (s *socks5Server) Start(ctx context.Context) error {
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
	s.log("info", "SOCKS5 代理已启动")

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

func (s *socks5Server) Stop() error {
	s.setStatus(StatusListenerStopped)
	return s.stop()
}

func (s *socks5Server) handleConn(conn net.Conn) {
	defer conn.Close()

	// 1. 握手协商认证方式
	// 客户端: VER(1) NMETHODS(1) METHODS(n)
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return
	}
	if header[0] != 0x05 {
		return
	}
	nmethods := int(header[1])
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}

	// 选择认证方式
	if s.helper.hasAuth() {
		// 需要用户名密码认证 (0x02)
		supportsUserPass := false
		for _, m := range methods {
			if m == 0x02 {
				supportsUserPass = true
				break
			}
		}
		if !supportsUserPass {
			conn.Write([]byte{0x05, 0xFF}) // 拒绝
			return
		}
		conn.Write([]byte{0x05, 0x02}) // 选中用户名密码
		if !s.doUserPassAuth(conn) {
			return
		}
	} else {
		// 无需认证 (0x00)
		conn.Write([]byte{0x05, 0x00})
	}

	// 2. 请求转发
	// VER(1) CMD(1) RSV(1) ATYP(1) DST.ADDR DST.PORT
	reqHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, reqHeader); err != nil {
		return
	}
	if reqHeader[0] != 0x05 {
		return
	}
	cmd := reqHeader[1]
	atyp := reqHeader[3]

	var host string
	var port uint16

	switch atyp {
	case 0x01: // IPv4
		ip := make([]byte, 4)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return
		}
		host = net.IP(ip).String()
	case 0x03: // 域名
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return
		}
		domain := make([]byte, int(lenBuf[0]))
		if _, err := io.ReadFull(conn, domain); err != nil {
			return
		}
		host = string(domain)
	case 0x04: // IPv6
		ip := make([]byte, 16)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return
		}
		host = net.IP(ip).String()
	default:
		s.reply(conn, 0x08, nil, 0) // 地址类型不支持
		return
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBuf); err != nil {
		return
	}
	port = binary.BigEndian.Uint16(portBuf)

	target := net.JoinHostPort(host, strconv.Itoa(int(port)))

	// 仅支持 CONNECT
	if cmd != 0x01 {
		s.reply(conn, 0x07, nil, 0) // 不支持的命令
		return
	}

	// 3. 通过 SSH 隧道连接目标
	targetConn, err := s.dialer.Dial("tcp", target)
	if err != nil {
		s.log("warn", "连接目标失败 "+target+": "+err.Error())
		s.reply(conn, 0x05, nil, 0) // 连接失败
		return
	}
	defer targetConn.Close()

	// 回复成功
	s.reply(conn, 0x00, net.IPv4(0, 0, 0, 0), 0)

	// 4. 双向桥接
	Bridge(conn, targetConn)
}

// doUserPassAuth RFC 1929 用户名密码子协商
func (s *socks5Server) doUserPassAuth(conn net.Conn) bool {
	// VER(1) ULEN(1) UNAME PLEN(1) PASSWD
	ver := make([]byte, 2)
	if _, err := io.ReadFull(conn, ver); err != nil {
		return false
	}
	ulen := int(ver[1])
	uname := make([]byte, ulen)
	if _, err := io.ReadFull(conn, uname); err != nil {
		return false
	}
	plenBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, plenBuf); err != nil {
		return false
	}
	passwd := make([]byte, int(plenBuf[0]))
	if _, err := io.ReadFull(conn, passwd); err != nil {
		return false
	}

	if !s.helper.checkSocks5(string(uname), string(passwd)) {
		conn.Write([]byte{0x01, 0x01}) // 失败
		s.log("warn", "SOCKS5 认证失败: "+string(uname))
		return false
	}
	conn.Write([]byte{0x01, 0x00}) // 成功
	return true
}

// reply 发送 SOCKS5 响应
func (s *socks5Server) reply(conn net.Conn, rep byte, bindIP net.IP, bindPort uint16) {
	resp := []byte{0x05, rep, 0x00}
	if bindIP == nil {
		bindIP = net.IPv4(0, 0, 0, 0)
	}
	if v4 := bindIP.To4(); v4 != nil {
		resp = append(resp, 0x01)
		resp = append(resp, v4...)
	} else {
		resp = append(resp, 0x04)
		resp = append(resp, bindIP.To16()...)
	}
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, bindPort)
	resp = append(resp, portBuf...)
	conn.Write(resp)
}

// 确保 socks5Server 实现 Server 接口
var _ Server = (*socks5Server)(nil)
