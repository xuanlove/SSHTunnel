package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"time"

	"sshtunnel/internal/config"
)

// httpServer HTTP/HTTPS 代理服务器
// 当 tlsCfg 为 nil 时为 HTTP 代理，否则为 HTTPS 代理（监听器包装 TLS）
type httpServer struct {
	*BaseServer
	auth   *config.AuthConfig
	helper authHelper
	tlsCfg *config.TLSConfig
}

func newHTTPServer(base *BaseServer, auth *config.AuthConfig, tlsCfg *config.TLSConfig) *httpServer {
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

	// HTTPS 代理：包装 TLS 监听器（证书以 PEM 文本形式配置）
	if s.tlsCfg != nil {
		if strings.TrimSpace(s.tlsCfg.CertPEM) == "" || strings.TrimSpace(s.tlsCfg.KeyPEM) == "" {
			ln.Close()
			s.setStatus(StatusListenerError)
			s.log("error", "未配置 HTTPS 证书内容（证书/私钥 PEM）")
			return errMissingTLSCert
		}
		cert, err := tls.X509KeyPair([]byte(s.tlsCfg.CertPEM), []byte(s.tlsCfg.KeyPEM))
		if err != nil {
			ln.Close()
			s.setStatus(StatusListenerError)
			s.log("error", "解析 TLS 证书失败: "+err.Error())
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
			time.Sleep(100 * time.Millisecond) // 持续错误（如 EMFILE）时退避，避免忙等
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
	s.stats.BeginConn()
	defer s.stats.EndConn()

	// 简易防火墙：来源校验
	if !s.allowConn(conn) {
		return
	}

	// 请求头读取限制超时，防止慢速连接长期占用 goroutine
	_ = conn.SetReadDeadline(time.Now().Add(s.readTimeout))
	if tc, ok := conn.(*tls.Conn); ok {
		ctx, cancel := context.WithTimeout(context.Background(), s.hsTimeout)
		err := tc.HandshakeContext(ctx)
		cancel()
		if err != nil {
			return
		}
	}

	start := time.Now()
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	// 限制请求头总大小，避免超大头部耗尽内存
	headerSize := len(req.Method) + len(req.URL.String()) + len(req.Proto)
	for k, vs := range req.Header {
		headerSize += len(k)
		for _, v := range vs {
			headerSize += len(v)
		}
	}
	if headerSize > s.maxHeader {
		conn.Write([]byte("HTTP/1.1 431 Request Header Fields Too Large\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"))
		s.logAccessDenied(conn, req.URL.Host, "请求头过大")
		return
	}

	// 请求头读取完成，清除超时（后续 CONNECT/响应可能长时间传输）
	_ = conn.SetReadDeadline(time.Time{})

	// 认证校验
	if !s.checkAuth(req) {
		resp := "HTTP/1.1 407 Proxy Authentication Required\r\n"
		resp += "Proxy-Authenticate: Basic realm=\"proxy\"\r\n"
		resp += "Content-Length: 0\r\n\r\n"
		conn.Write([]byte(resp))
		s.log("warn", "HTTP 认证失败")
		// HTTP 请求已解析，目标地址已知，拒绝记录中一并输出
		s.logAccessDenied(conn, req.URL.Host, "认证失败")
		return
	}

	// 请求已解析、目标已知：登记为活动连接（桥接结束时注销）
	entry := s.trackConn(conn, req.URL.Host)
	defer s.closeConn(entry)

	// CONNECT 方法（HTTPS 隧道）
	if req.Method == http.MethodConnect {
		// 回灌 bufio 预读的数据，避免客户端提前发送的 TLS 首包被丢弃
		s.handleConnect(&bufferedConn{r: br, Conn: conn}, req, start, entry)
		return
	}

	// 普通 HTTP 请求转发
	s.handleHTTP(conn, req, start, entry)
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
func (s *httpServer) handleConnect(conn net.Conn, req *http.Request, start time.Time, entry *ConnEntry) {
	target := req.URL.Host
	targetConn, err := s.dialer.Dial("tcp", target)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		s.log("warn", "CONNECT 连接失败 "+target+": "+err.Error())
		s.logAccess(conn, target, start, "失败: 连接目标失败")
		return
	}
	defer targetConn.Close()

	// 回复 200
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// 双向桥接
	BridgeStats(conn, targetConn, s.stats, entry)
	s.logAccess(conn, target, start, "成功")
}

// handleHTTP 处理普通 HTTP 请求转发
func (s *httpServer) handleHTTP(conn net.Conn, req *http.Request, start time.Time, entry *ConnEntry) {
	target := req.URL.Host
	// 绝对形式请求可省略默认端口（如 GET http://example.com/ HTTP/1.1），补默认 80
	if req.URL.Port() == "" {
		target = net.JoinHostPort(req.URL.Hostname(), "80")
	}
	// 通过 SSH 隧道连接目标
	targetConn, err := s.dialer.Dial("tcp", target)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		s.log("warn", "HTTP 连接失败 "+target+": "+err.Error())
		s.logAccess(conn, target, start, "失败: 连接目标失败")
		return
	}
	defer targetConn.Close()

	// 移除逐跳头部，添加 Via 头（本代理仅处理单次请求，响应后关闭连接）
	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	req.Header.Del("Connection")
	req.Header.Del("Keep-Alive")
	req.Header.Del("TE")
	req.Header.Del("Trailer")
	req.Header.Del("Upgrade")
	req.Header.Set("Via", "1.1 sshtunnel")

	// 仅统计请求/响应体字节（与 SOCKS、本地转发的负载口径一致）
	if req.Body != nil {
		req.Body = &countReadCloser{r: req.Body, stats: s.stats, conn: entry, sent: true}
	}
	// 转发请求
	if err := req.Write(targetConn); err != nil {
		s.logAccess(conn, target, start, "失败: 发送请求失败")
		return
	}

	// 转发响应
	tr := bufio.NewReader(targetConn)
	resp, err := http.ReadResponse(tr, req)
	if err != nil {
		s.logAccess(conn, target, start, "失败: 读取响应失败")
		return
	}
	defer resp.Body.Close()

	resp.Header.Del("Connection")
	resp.Header.Del("Keep-Alive")
	resp.Header.Del("Proxy-Connection")
	resp.Header.Del("TE")
	resp.Header.Del("Trailer")
	resp.Header.Del("Upgrade")
	resp.Header.Set("Via", "1.1 sshtunnel")
	resp.Close = true
	resp.Body = &countReadCloser{r: resp.Body, stats: s.stats, conn: entry}
	if err := resp.Write(&idleWriter{conn: conn, timeout: idleWriteTimeout}); err != nil {
		s.logAccess(conn, target, start, "失败: 回写响应失败")
		return
	}

	s.logAccess(conn, target, start, "成功")
}

// 确保 httpServer 实现 Server 接口
var _ Server = (*httpServer)(nil)
