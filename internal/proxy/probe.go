package proxy

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// DefaultProbeTarget 代理连通性探测的默认目标地址（返回访问方出口 IP 的纯文本接口）
const DefaultProbeTarget = "https://api.ip.sb/ip"

// ProbeOptions 代理连通性探测参数
type ProbeOptions struct {
	Protocol string        // 代理协议：http | https | socks4 | socks5
	Host     string        // 代理主机（默认 127.0.0.1）
	Port     int           // 代理监听端口
	Username string        // 代理认证用户名（可选）
	Password string        // 代理认证密码（可选）
	Target   string        // 目标 URL（默认 DefaultProbeTarget）
	Timeout  time.Duration // 总超时（默认 15s）
}

// ProbeResult 代理连通性探测结果
type ProbeResult struct {
	IP        string `json:"ip"`         // 解析出的出口 IP（目标返回纯文本时即为响应体）
	Raw       string `json:"raw"`        // 原始响应体（截断至 200 字符）
	ElapsedMS int64  `json:"elapsed_ms"` // 总耗时（毫秒）
	Target    string `json:"target"`     // 实际访问的目标地址
}

// Probe 经指定代理监听端口访问目标地址，验证代理链路连通性并返回出口 IP。
// 适用于校验已启动的代理监听器（HTTP/HTTPS/SOCKS4/SOCKS5）是否真正走通隧道。
func Probe(opts ProbeOptions) (*ProbeResult, error) {
	if opts.Host == "" {
		opts.Host = "127.0.0.1"
	}
	if opts.Target == "" {
		opts.Target = DefaultProbeTarget
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Second
	}
	if opts.Port < 1 || opts.Port > 65535 {
		return nil, fmt.Errorf("无效的代理端口: %d", opts.Port)
	}

	u, err := url.Parse(opts.Target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, fmt.Errorf("无效的目标地址: %s", opts.Target)
	}
	targetPort := 80
	if u.Scheme == "https" {
		targetPort = 443
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			targetPort = n
		}
	}

	start := time.Now()

	// 1. 建立到目标的连接（经由代理）
	conn, err := dialViaProxy(opts, u.Hostname(), targetPort)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	// 2. 总超时
	if err := conn.SetDeadline(time.Now().Add(opts.Timeout)); err != nil {
		return nil, err
	}

	// 3. HTTPS 目标：在隧道之上再建 TLS（目标证书正常校验）
	if u.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: u.Hostname()})
		if err := tlsConn.Handshake(); err != nil {
			return nil, fmt.Errorf("与目标 %s 建立 TLS 失败: %w", u.Hostname(), err)
		}
		conn = tlsConn
	}

	// 4. 发送 HTTP GET
	reqLine := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUser-Agent: sshtunnel-probe\r\nAccept: */*\r\nConnection: close\r\n\r\n",
		u.RequestURI(), u.Host,
	)
	if _, err := conn.Write([]byte(reqLine)); err != nil {
		return nil, fmt.Errorf("发送请求失败: %w", err)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		return nil, fmt.Errorf("读取目标响应失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	raw := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("目标返回 %s: %s", resp.Status, truncate(raw, 120))
	}

	ip := raw
	if ip == "" {
		ip = "(空响应)"
	}
	return &ProbeResult{
		IP:        ip,
		Raw:       truncate(raw, 200),
		ElapsedMS: time.Since(start).Milliseconds(),
		Target:    opts.Target,
	}, nil
}

// dialViaProxy 按协议建立到 targetHost:targetPort 的连接
func dialViaProxy(opts ProbeOptions, targetHost string, targetPort int) (net.Conn, error) {
	proxyAddr := net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port))
	targetAddr := net.JoinHostPort(targetHost, strconv.Itoa(targetPort))
	d := &net.Dialer{Timeout: opts.Timeout}

	switch opts.Protocol {
	case "socks5", "":
		var auth *xproxy.Auth
		if opts.Username != "" || opts.Password != "" {
			auth = &xproxy.Auth{User: opts.Username, Password: opts.Password}
		}
		dialer, err := xproxy.SOCKS5("tcp", proxyAddr, auth, d)
		if err != nil {
			return nil, fmt.Errorf("创建 SOCKS5 拨号器失败: %w", err)
		}
		conn, err := dialer.Dial("tcp", targetAddr)
		if err != nil {
			return nil, fmt.Errorf("经 SOCKS5 代理 %s 连接目标失败（监听是否已启动？）: %w", proxyAddr, err)
		}
		return conn, nil

	case "socks4":
		return dialSOCKS4(d, proxyAddr, opts.Username, targetHost, targetPort)

	case "http", "https":
		return dialHTTPConnect(d, proxyAddr, opts.Protocol == "https", opts, targetAddr)
	}

	return nil, fmt.Errorf("不支持的代理协议: %s", opts.Protocol)
}

// dialSOCKS4 通过 SOCKS4/SOCKS4a 建立连接（支持域名直连）
func dialSOCKS4(d *net.Dialer, proxyAddr, userID, targetHost string, targetPort int) (net.Conn, error) {
	conn, err := d.Dial("tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("连接 SOCKS4 代理 %s 失败（监听是否已启动？）: %w", proxyAddr, err)
	}

	req := []byte{0x04, 0x01}
	portBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(portBuf, uint16(targetPort))
	req = append(req, portBuf...)

	if ip4 := net.ParseIP(targetHost).To4(); ip4 != nil {
		// SOCKS4：IPv4 + UserID
		req = append(req, ip4...)
		req = append(req, []byte(userID)...)
		req = append(req, 0x00)
	} else {
		// SOCKS4a：0.0.0.1 + UserID 终止符 + 域名
		req = append(req, 0x00, 0x00, 0x00, 0x01)
		req = append(req, []byte(userID)...)
		req = append(req, 0x00)
		req = append(req, []byte(targetHost)...)
		req = append(req, 0x00)
	}

	if _, err := conn.Write(req); err != nil {
		conn.Close()
		return nil, fmt.Errorf("SOCKS4 握手发送失败: %w", err)
	}
	resp := make([]byte, 8)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, fmt.Errorf("SOCKS4 握手响应读取失败: %w", err)
	}
	if resp[1] != 0x5a {
		conn.Close()
		return nil, fmt.Errorf("SOCKS4 代理拒绝连接 (code=0x%02x)", resp[1])
	}
	return conn, nil
}

// dialHTTPConnect 通过 HTTP/HTTPS 代理的 CONNECT 方法建立隧道
func dialHTTPConnect(d *net.Dialer, proxyAddr string, useTLS bool, opts ProbeOptions, targetAddr string) (net.Conn, error) {
	conn, err := d.Dial("tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("连接 %s 代理 %s 失败（监听是否已启动？）: %w", opts.Protocol, proxyAddr, err)
	}

	if useTLS {
		// 代理自身证书常为自签（本项目一键生成），此处不校验代理证书；
		// 隧道内目标证书在 Probe 中单独正常校验。
		tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true, ServerName: opts.Host})
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, fmt.Errorf("与 HTTPS 代理建立 TLS 失败: %w", err)
		}
		conn = tlsConn
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", targetAddr, targetAddr)
	if opts.Username != "" || opts.Password != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(opts.Username + ":" + opts.Password))
		fmt.Fprintf(&sb, "Proxy-Authorization: Basic %s\r\n", cred)
	}
	sb.WriteString("Proxy-Connection: Keep-Alive\r\n\r\n")
	if _, err := conn.Write([]byte(sb.String())); err != nil {
		conn.Close()
		return nil, fmt.Errorf("发送 CONNECT 请求失败: %w", err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("读取代理响应失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
		if resp.StatusCode == http.StatusProxyAuthRequired {
			return nil, fmt.Errorf("代理要求认证（请检查用户名密码）")
		}
		return nil, fmt.Errorf("代理 CONNECT 失败: %s", resp.Status)
	}

	// 若缓冲区仍有残留数据，先读取缓冲区再读连接
	if br.Buffered() > 0 {
		return &bufferedConn{r: br, Conn: conn}, nil
	}
	return conn, nil
}

// bufferedConn 优先从缓冲区读取，避免吞掉代理响应后残留的数据
type bufferedConn struct {
	r io.Reader
	net.Conn
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// truncate 截断字符串到 max 字节（按 rune 安全截断）
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	runes := []rune(s)
	if len(runes) > max {
		return string(runes[:max]) + "..."
	}
	return s[:max] + "..."
}