package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"

	"sshtunnel/internal/config"
)

// httpServer HTTP/HTTPS 代理服务器
// 当 tlsCfg 为 nil 时为 HTTP 代理，否则为 HTTPS 代理（监听器包装 TLS）
type httpServer struct {
	BaseServer
	auth    *config.AuthConfig
	helper  authHelper
	tlsCfg  *config.TLSConfig
}

func newHTTPServer(base BaseServer, auth *config.AuthConfig, tlsCfg *config.TLSConfig) *httpServer {
	return &httpServer{
		BaseServer: base,
		auth:       auth,
		helper:     authHelper{auth: auth},
		tlsCfg:     tlsCfg,
	}
}

func (s *httpServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		s.setStatus(StatusListenerError)
		s.log("error", "监听失败: "+err.Error())
		return err
	}

	// HTTPS 代理：包装 TLS 监听器
	if s.tlsCfg != nil {
		cert, err := tls.LoadX509KeyPair(s.tlsCfg.CertFile, s.tlsCfg.KeyFile)
		if err != nil {
			ln.Close()
			s.setStatus(StatusListenerError)
			s.log("error", "加载 TLS 证书失败: "+err.Error())
			return err
		}
		ln = tls.NewListener(ln, &tls.Config{
			Certificates: []tls.Certificate{cert},
		})
	}

	s.mu.Lock()
	s.listener = ln
	s.status = StatusListenerRunning
	s.mu.Unlock()

	proto := "HTTP"
	if s.tlsCfg != nil {
		proto = "HTTPS"
	}
	s.log("info", proto+" 代理已启动")

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

func (s *httpServer) Stop() error {
	s.setStatus(StatusListenerStopped)
	return s.stop()
}

func (s *httpServer) handleConn(conn net.Conn) {
	defer conn.Close()

	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	// 认证校验
	if !s.checkAuth(req) {
		resp := "HTTP/1.1 407 Proxy Authentication Required\r\n"
		resp += "Proxy-Authenticate: Basic realm=\"proxy\"\r\n"
		resp += "Content-Length: 0\r\n\r\n"
		conn.Write([]byte(resp))
		s.log("warn", "HTTP 认证失败")
		return
	}

	// CONNECT 方法（HTTPS 隧道）
	if req.Method == http.MethodConnect {
		s.handleConnect(conn, req)
		return
	}

	// 普通 HTTP 请求转发
	s.handleHTTP(conn, req)
}

// checkAuth HTTP Basic Auth 校验
func (s *httpServer) checkAuth(req *http.Request) bool {
	if !s.helper.hasAuth() {
		return true
	}
	authHeader := req.Header.Get("Proxy-Authorization")
	if authHeader == "" {
		return false
	}
	const prefix = "Basic "
	if !strings.HasPrefix(authHeader, prefix) {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(authHeader[len(prefix):])
	if err != nil {
		return false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return false
	}
	return s.helper.checkBasicAuth(parts[0], parts[1])
}

// handleConnect 处理 CONNECT 方法（HTTPS 隧道）
func (s *httpServer) handleConnect(conn net.Conn, req *http.Request) {
	targetConn, err := s.dialer.Dial("tcp", req.URL.Host)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		s.log("warn", "CONNECT 连接失败 "+req.URL.Host+": "+err.Error())
		return
	}
	defer targetConn.Close()

	// 回复 200
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// 双向桥接
	Bridge(conn, targetConn)
}

// handleHTTP 处理普通 HTTP 请求转发
func (s *httpServer) handleHTTP(conn net.Conn, req *http.Request) {
	// 通过 SSH 隧道连接目标
	targetConn, err := s.dialer.Dial("tcp", req.URL.Host)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		s.log("warn", "HTTP 连接失败 "+req.URL.Host+": "+err.Error())
		return
	}
	defer targetConn.Close()

	// 移除逐跳头部，添加 Via 头
	req.Header.Del("Proxy-Authorization")
	req.Header.Set("Via", "1.1 sshtunnel")

	// 转发请求
	if err := req.Write(targetConn); err != nil {
		return
	}

	// 转发响应
	tr := bufio.NewReader(targetConn)
	resp, err := http.ReadResponse(tr, req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	resp.Header.Set("Via", "1.1 sshtunnel")
	if err := resp.Write(conn); err != nil {
		return
	}

	// 刷新剩余数据
	_, _ = io.Copy(conn, tr)
}

// 确保 httpServer 实现 Server 接口
var _ Server = (*httpServer)(nil)
