package sshclient

import (
	"fmt"
	"net"
	"strconv"
	"strings"
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
		if colon := strings.LastIndex(part, ":"); colon >= 0 {
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
	client *ssh.Client
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
	addr := fmt.Sprintf("%s:%d", first.Host, first.Port)
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("第一跳连接失败 %s: %w", addr, err)
	}

	// 后续跳板通过前一跳 Dial 串联
	for i := 1; i < len(hops); i++ {
		hop := hops[i]
		nextAuth, err := buildAuth(hop)
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("第 %d 跳认证配置失败: %w", i+1, err)
		}
		nextAddr := fmt.Sprintf("%s:%d", hop.Host, hop.Port)
		conn, err := client.Dial("tcp", nextAddr)
		if err != nil {
			client.Close()
			return nil, fmt.Errorf("第 %d 跳连接失败 %s: %w", i+1, nextAddr, err)
		}
		nextCfg := &ssh.ClientConfig{
			User:            hop.User,
			Auth:            nextAuth,
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         15 * time.Second,
		}
		ncc, chans, reqs, err := ssh.NewClientConn(conn, nextAddr, nextCfg)
		if err != nil {
			conn.Close()
			client.Close()
			return nil, fmt.Errorf("第 %d 跳握手失败: %w", i+1, err)
		}
		client = ssh.NewClient(ncc, chans, reqs)
	}

	return &Client{client: client}, nil
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
	if c.client == nil {
		return nil, fmt.Errorf("ssh client closed")
	}
	return c.client.Dial(network, address)
}

// Wait 等待连接结束（用于监听断开事件）
func (c *Client) Wait() error {
	return c.client.Wait()
}

// Close 关闭连接
func (c *Client) Close() error {
	if c.client == nil {
		return nil
	}
	return c.client.Close()
}
