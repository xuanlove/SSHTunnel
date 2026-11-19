package sshclient

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// startTestSSHServer 启动一个最小 SSH 服务端：
// 心跳请求（keepalive@openssh.com）到达时主动关闭连接，用于模拟心跳失败。
func startTestSSHServer(t *testing.T) (port int, stop func()) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("生成主机密钥失败: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("创建签名器失败: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			return nil, nil // 测试环境接受任意密码
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func(nc net.Conn) {
				sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go func() {
					for ch := range chans {
						_ = ch.Reject(ssh.Prohibited, "unsupported")
					}
				}()
				for req := range reqs {
					if req.Type == "keepalive@openssh.com" {
						_ = sc.Close() // 模拟心跳失败
						return
					}
					if req.WantReply {
						_ = req.Reply(false, nil)
					}
				}
			}(nc)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, func() { _ = ln.Close() }
}

// TestKeepAliveFailureClosesClient 心跳失败应关闭连接，使 Wait 返回以触发上层重连。
func TestKeepAliveFailureClosesClient(t *testing.T) {
	port, stopServer := startTestSSHServer(t)
	defer stopServer()

	c, err := Dial([]Hop{{
		User:     "test",
		Host:     "127.0.0.1",
		Port:     port,
		AuthType: "password",
		Password: "test",
	}})
	if err != nil {
		t.Fatalf("连接测试服务器失败: %v", err)
	}
	defer c.Close()

	stopHB := c.StartKeepAlive(10 * time.Millisecond)
	defer stopHB()

	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
		// 心跳失败后连接应断开
	case <-time.After(3 * time.Second):
		t.Fatal("心跳失败后连接未被关闭")
	}
}

// TestKeepAliveDisabled 间隔为 0 时不启动心跳，连接应保持可用。
func TestKeepAliveDisabled(t *testing.T) {
	port, stopServer := startTestSSHServer(t)
	defer stopServer()

	c, err := Dial([]Hop{{
		User:     "test",
		Host:     "127.0.0.1",
		Port:     port,
		AuthType: "password",
		Password: "test",
	}})
	if err != nil {
		t.Fatalf("连接测试服务器失败: %v", err)
	}
	defer c.Close()

	stopHB := c.StartKeepAlive(0)
	defer stopHB()

	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-done:
		t.Fatal("关闭心跳时连接不应自动断开")
	case <-time.After(200 * time.Millisecond):
		// 正常：连接仍保持
	}

	_ = c.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("主动关闭后 Wait 应返回")
	}
}
