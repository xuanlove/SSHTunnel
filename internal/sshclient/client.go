package sshclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log"
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
	HostKey    string // 主机密钥指纹/公钥（SHA256:...、base64 公钥或 authorized_keys 行）
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

const hopTimeout = 15 * time.Second

// Dial 建立到最终目标的 SSH 连接（串联所有跳板）。
func Dial(hops []Hop) (*Client, error) {
	return DialContext(context.Background(), hops)
}

// DialContext 为每跳的 TCP 拨号和 SSH 握手提供期限，并在取消时关闭底层连接。
func DialContext(ctx context.Context, hops []Hop) (*Client, error) {
	return dialChain(ctx, hops, hopTimeout)
}

func dialChain(ctx context.Context, hops []Hop, timeout time.Duration) (*Client, error) {
	if len(hops) == 0 {
		return nil, fmt.Errorf("跳板链为空")
	}

	var root net.Conn
	var client *ssh.Client
	var intermediates []*ssh.Client
	for i, hop := range hops {
		addr := net.JoinHostPort(hop.Host, strconv.Itoa(hop.Port))
		expected, err := NormalizeHostKey(hop.HostKey)
		if err != nil {
			closeClients(client, intermediates)
			return nil, fmt.Errorf("第 %d 跳 %s 主机密钥配置无效: %w", i+1, addr, err)
		}
		auth, err := buildAuth(hop)
		if err != nil {
			closeClients(client, intermediates)
			return nil, fmt.Errorf("第 %d 跳认证配置失败: %w", i+1, err)
		}
		hopCtx, cancel := context.WithTimeout(ctx, timeout)
		if root == nil {
			root, err = (&net.Dialer{}).DialContext(hopCtx, "tcp", addr)
			if err != nil {
				cancel()
				return nil, fmt.Errorf("第 %d 跳连接失败 %s: %w", i+1, addr, err)
			}
		}
		next, err := connectHop(hopCtx, root, client, addr, &ssh.ClientConfig{
			User: hop.User, Auth: auth, HostKeyCallback: hostKeyCallback(expected),
		})
		cancel()
		if err != nil {
			_ = root.Close()
			closeClients(client, intermediates)
			return nil, fmt.Errorf("第 %d 跳连接失败 %s: %w%s", i+1, addr, err, authHint(err))
		}
		if client != nil {
			intermediates = append(intermediates, client)
		}
		client = next
	}
	return &Client{client: client, intermediates: intermediates}, nil
}

func connectHop(ctx context.Context, root net.Conn, previous *ssh.Client, addr string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	// SSH 转发通道无法设置 deadline。取消时关闭承载整个跳板链的 TCP 连接。
	stop := context.AfterFunc(ctx, func() { _ = root.Close() })
	connect := func() (*ssh.Client, error) {
		conn := root
		if previous != nil {
			var err error
			conn, err = previous.DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
		}
		sshConn, channels, requests, err := ssh.NewClientConn(conn, addr, cfg)
		if err != nil {
			return nil, err
		}
		return ssh.NewClient(sshConn, channels, requests), nil
	}
	client, err := connect()
	if !stop() || ctx.Err() != nil {
		_ = root.Close()
		return nil, ctx.Err()
	}
	return client, err
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
		return []ssh.AuthMethod{
			ssh.Password(hop.Password),
			// 部分服务器禁用密码认证但保留键盘交互认证（KbdInteractiveAuthentication/PAM），
			// 以相同密码应答键盘交互挑战作为回退
			ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range questions {
					answers[i] = hop.Password
				}
				return answers, nil
			}),
		}, nil
	}
}

// authHint 对「认证被拒」类错误附加可操作的排查提示
func authHint(err error) string {
	if err != nil && strings.Contains(err.Error(), "unable to authenticate") {
		return "（认证被服务器拒绝：请确认用户名与凭据正确，且服务器对该用户允许密码/键盘交互/公钥认证——若使用本项目生成的最小权限账户，请检查 sshd Match 块中的认证方式与客户端所选认证方式一致）"
	}
	return ""
}

// hostKeyCallback 返回主机密钥校验回调。
// expected 为空表示未配置：跳过校验（沿用历史行为）并输出一次安全提示；
// 配置后严格比对 SHA256 指纹，防止中间人攻击。
func hostKeyCallback(expected string) ssh.HostKeyCallback {
	if expected == "" {
		insecureHostKeyWarn.Do(func() {
			log.Printf("[WARN] 未配置主机密钥指纹（host_key），不校验服务器主机密钥，存在中间人风险；建议在跳板配置中填写 SHA256 指纹")
		})
		return ssh.InsecureIgnoreHostKey()
	}
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		if got := fingerprintKey(key); got != expected {
			return fmt.Errorf("主机密钥校验失败 %s: 期望 %s，实际 %s（服务器密钥已更换或存在中间人攻击）",
				hostname, expected, got)
		}
		return nil
	}
}

// insecureHostKeyWarn 保证未配置 host_key 的提示每进程只输出一次，避免每次拨号刷屏
var insecureHostKeyWarn sync.Once

// fingerprintKey 计算 SSH 公钥的 SHA256 指纹（与 ssh-keygen 输出一致：SHA256:base64 无 padding）
func fingerprintKey(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// NormalizeHostKey 将用户输入的主机密钥规范化为 SHA256 指纹（SHA256:base64 无 padding）。
// 空输入返回空串（表示未配置，不校验主机密钥）。支持三种输入：
//   - OpenSSH 指纹：SHA256:xxxx（允许带 padding，统一去除）
//   - base64 公钥裸串：xxxx（如 ssh-keygen -lf 输出的第二列）
//   - authorized_keys 行：ssh-ed25519 xxxx comment（取第二列）
func NormalizeHostKey(input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", nil
	}
	fields := strings.Fields(s)
	if strings.HasPrefix(fields[0], "SHA256:") {
		fp := strings.TrimPrefix(fields[0], "SHA256:")
		raw, err := decodeBase64(fp)
		if err != nil || len(raw) == 0 {
			return "", fmt.Errorf("host_key 指纹不是合法的 base64: %q", fp)
		}
		return "SHA256:" + base64.RawStdEncoding.EncodeToString(raw), nil
	}
	blob := fields[0]
	if len(fields) > 1 {
		blob = fields[1] // authorized_keys 行：跳过第一列密钥类型
	}
	raw, err := decodeBase64(blob)
	if err != nil || len(raw) == 0 {
		return "", fmt.Errorf("host_key 不是合法的指纹或公钥: %q", blob)
	}
	sum := sha256.Sum256(raw)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

// decodeBase64 兼容标准/URL 字母表与是否带 padding 的 base64 解码
func decodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.RawStdEncoding, base64.StdEncoding,
		base64.RawURLEncoding, base64.URLEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil && len(b) > 0 {
			return b, nil
		}
	}
	return nil, fmt.Errorf("无法解码 base64")
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
