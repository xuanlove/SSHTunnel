package sshclient

import (
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
	HostKey    string // 主机密钥指纹/公钥（SHA256:...、base64 公钥或 authorized_keys 行；空表示跳过校验）
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
		HostKeyCallback: hostKeyCallback(first),
		Timeout:         15 * time.Second,
	}
	addr := net.JoinHostPort(first.Host, strconv.Itoa(first.Port))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, fmt.Errorf("第一跳连接失败 %s: %w%s", addr, err, authHint(err))
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
			HostKeyCallback: hostKeyCallback(hop),
			Timeout:         15 * time.Second,
		}
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
		ncc, chans, reqs, err := ssh.NewClientConn(conn, nextAddr, nextCfg)
		_ = conn.SetDeadline(time.Time{})
		if err != nil {
			_ = conn.Close()
			closeClients(client, intermediates)
			return nil, fmt.Errorf("第 %d 跳握手失败: %w%s", i+1, err, authHint(err))
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

// insecureKeyWarnOnce 保证「未配置 host_key」的安全提示每进程只输出一次，避免每次拨号刷屏
var insecureKeyWarnOnce sync.Once

// hostKeyCallback 返回该跳的主机密钥回调：
//   - 配置了 host_key：按 sha256(公钥 blob) 指纹严格校验，不匹配则拒绝连接
//   - 未配置：保持旧行为跳过校验，并输出一次可配置 host_key 的安全提示
func hostKeyCallback(hop Hop) ssh.HostKeyCallback {
	if hop.HostKey == "" {
		return func(string, net.Addr, ssh.PublicKey) error {
			insecureKeyWarnOnce.Do(func() {
				log.Println("[warn] SSH 主机密钥未校验（存在中间人风险）：可在跳板配置中填写 host_key 主机密钥指纹（SHA256:...）启用校验")
			})
			return nil
		}
	}
	expected, cfgErr := NormalizeHostKey(hop.HostKey)
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		if cfgErr != nil {
			return fmt.Errorf("host_key 配置无效: %w", cfgErr)
		}
		if got := fingerprintKey(key); got != expected {
			return fmt.Errorf("主机密钥校验失败 %s: 期望 %s，实际 %s（服务器密钥已更换或存在中间人攻击）",
				hostname, expected, got)
		}
		return nil
	}
}

// fingerprintKey 计算 SSH 公钥的 SHA256 指纹（与 ssh-keygen 输出一致：SHA256:base64 无 padding）
func fingerprintKey(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// NormalizeHostKey 将用户输入的主机密钥规范化为 SHA256 指纹（SHA256:base64 无 padding）。
// 支持三种输入：
//   - OpenSSH 指纹：SHA256:xxxx（允许带 padding，统一去除）
//   - base64 公钥裸串：xxxx（如 ssh-keygen -lf 输出的第二列）
//   - authorized_keys 行：ssh-ed25519 xxxx comment（取第二列）
func NormalizeHostKey(input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", fmt.Errorf("host_key 为空")
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
