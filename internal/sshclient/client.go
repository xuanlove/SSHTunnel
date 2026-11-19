package sshclient

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Hop 单跳配置
type Hop struct {
	User       string
	Host       string
	Port       int
	AuthType   string // password | key
	Password   string
	KeyContent string // 密钥文本内容（PEM 格式）
	Passphrase string
}

// ParseHopChain 解析简写格式，支持 , 和 -> 两种分隔符
// 例如: user1@host1:port1,user2@host2:port2 或 user1@host1:port1 -> user2@host2:port2
func ParseHopChain(s string) []Hop {
	// 统一将 -> 替换为 , 作为分隔符
	s = strings.ReplaceAll(s, "->", ",")
	var hops []Hop
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		h := Hop{Port: 22}
		if at := strings.Index(part, "@"); at >= 0 {
			h.User = part[:at]
			part = part[at+1:]
		}
		// 方括号 IPv6：[::1] 或 [::1]:22
		if strings.HasPrefix(part, "[") {
			if end := strings.Index(part, "]"); end >= 0 {
				h.Host = part[1:end]
				rest := part[end+1:]
				if strings.HasPrefix(rest, ":") {
					if p, err := strconv.Atoi(rest[1:]); err == nil {
						h.Port = p
					}
				}
				hops = append(hops, h)
				continue
			}
		}
		// 仅一个冒号视为 host:port；多个冒号为纯 IPv6（无端口）
		if colon := strings.LastIndex(part, ":"); colon >= 0 && strings.Count(part, ":") == 1 {
			if p, err := strconv.Atoi(part[colon+1:]); err == nil {
				h.Port = p
				part = part[:colon]
			}
		}
		h.Host = part
		hops = append(hops, h)
	}
	return hops
}

// Client SSH 客户端，支持多跳串联
type Client struct {
	mu            sync.Mutex
	client        *ssh.Client
	intermediates []*ssh.Client // 中间跳（最终跳关闭后仍需一并关闭，避免连接泄漏）
}

// Dial 建立到最终目标的 SSH 连接（串联所有跳板）
func Dial(hops []Hop) (*Client, error) {
	if len(hops) == 0 {
		return nil, fmt.Errorf("跳板链为空")
	}

	// 第一跳直接连接
	first := hops[0]
	auth, err := buildAuth(first)
	if err != nil {
		return nil, fmt.Errorf("第一跳认证配置失败: %w", err)
	}
	cfg := &ssh.ClientConfig{
		User:            first.User,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(first.Host, strconv.Itoa(first.Port))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("第一跳连接失败 %s: %w", addr, err)
	}

	// 后续跳板通过前一跳 Dial 串联
	var intermediates []*ssh.Client
	for i := 1; i < len(hops); i++ {
		hop := hops[i]
		nextAuth, err := buildAuth(hop)
		if err != nil {
			closeClients(client, intermediates)
			return nil, fmt.Errorf("第 %d 跳认证配置失败: %w", i+1, err)
		}
		nextAddr := net.JoinHostPort(hop.Host, strconv.Itoa(hop.Port))
		conn, err := dialWithTimeout(client, nextAddr, 15*time.Second)
		if err != nil {
			closeClients(client, intermediates)
			return nil, fmt.Errorf("第 %d 跳连接失败 %s: %w", i+1, nextAddr, err)
		}
		nextCfg := &ssh.ClientConfig{
			User:            hop.User,
			Auth:            nextAuth,
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         15 * time.Second,
		}
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		ncc, chans, reqs, err := ssh.NewClientConn(conn, nextAddr, nextCfg)
		_ = conn.SetDeadline(time.Time{})
		if err != nil {
			_ = conn.Close()
			closeClients(client, intermediates)
			return nil, fmt.Errorf("第 %d 跳握手失败: %w", i+1, err)
		}
		intermediates = append(intermediates, client)
		client = ssh.NewClient(ncc, chans, reqs)
	}

	return &Client{client: client, intermediates: intermediates}, nil
}

// dialWithTimeout 在指定超时内通过当前 SSH 连接拨号；超时后关闭当前连接以中断阻塞
func dialWithTimeout(c *ssh.Client, addr string, timeout time.Duration) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := c.Dial("tcp", addr)
		ch <- result{conn: conn, err: err}
	}()
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-time.After(timeout):
		// 本次多跳已失败：关闭当前跳使阻塞中的 Dial 退出
		_ = c.Close()
		return nil, fmt.Errorf("连接 %s 超时", addr)
	}
}

// closeClients 逆序关闭当前与中间跳
func closeClients(current *ssh.Client, intermediates []*ssh.Client) {
	if current != nil {
		_ = current.Close()
	}
	for i := len(intermediates) - 1; i >= 0; i-- {
		_ = intermediates[i].Close()
	}
}

func buildAuth(hop Hop) ([]ssh.AuthMethod, error) {
	switch hop.AuthType {
	case "key":
		if hop.KeyContent == "" {
			return nil, fmt.Errorf("密钥内容为空")
		}
		data := []byte(hop.KeyContent)
		var signer ssh.Signer
		var err error
		if hop.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(hop.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(data)
		}
		if err != nil {
			return nil, fmt.Errorf("解析密钥失败: %w", err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default: // password
		return []ssh.AuthMethod{ssh.Password(hop.Password)}, nil
	}
}

// Dial 通过 SSH 隧道连接远程地址（供代理与本地转发复用）
func (c *Client) Dial(network, address string) (net.Conn, error) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("ssh client closed")
	}
	return client.Dial(network, address)
}

// Wait 等待连接结束（用于监听断开事件）
func (c *Client) Wait() error {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return fmt.Errorf("ssh client closed")
	}
	return client.Wait()
}

// Close 关闭连接（含多跳中间连接）
func (c *Client) Close() error {
	c.mu.Lock()
	client := c.client
	intermediates := c.intermediates
	c.client = nil
	c.intermediates = nil
	c.mu.Unlock()

	var err error
	if client != nil {
		err = client.Close()
	}
	for i := len(intermediates) - 1; i >= 0; i-- {
		_ = intermediates[i].Close()
	}
	return err
}

// StartKeepAlive 启动 SSH 心跳（keepalive@openssh.com），返回停止函数。
// 间隔 <= 0 时不做任何事。心跳失败会关闭连接，从而触发上层自动重连。
func (c *Client) StartKeepAlive(interval time.Duration) func() {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if interval <= 0 || client == nil {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					_ = c.Close()
					return
				}
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}
