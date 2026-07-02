package main

import (
	"sshtunnel/internal/config"
	"sshtunnel/internal/sshclient"
)

// simpleHop 用于测试连接的中间结构
type simpleHop struct {
	User       string
	Host       string
	Port       int
	AuthType   string
	Password   string
	KeyContent string
	Passphrase string
}

// parseSimpleHopChain 解析简写格式 user1@host1:port1,user2@host2:port2
func parseSimpleHopChain(s string) []simpleHop {
	parsed := sshclient.ParseHopChain(s)
	out := make([]simpleHop, len(parsed))
	for i, h := range parsed {
		out[i] = simpleHop{
			User: h.User,
			Host: h.Host,
			Port: h.Port,
		}
	}
	return out
}

// testSSHConnection 测试 SSH 多跳连通性（连接后立即关闭）
func testSSHConnection(hops []simpleHop) error {
	sshHops := make([]sshclient.Hop, len(hops))
	for i, h := range hops {
		sshHops[i] = sshclient.Hop{
			User:       h.User,
			Host:       h.Host,
			Port:       h.Port,
			AuthType:   h.AuthType,
			Password:   h.Password,
			KeyContent: h.KeyContent,
			Passphrase: h.Passphrase,
		}
	}
	client, err := sshclient.Dial(sshHops)
	if err != nil {
		return err
	}
	return client.Close()
}

// cfgHopsToSSH 转换配置结构到 SSH 客户端结构
func cfgHopsToSSH(cfg []config.HopConfig) []sshclient.Hop {
	out := make([]sshclient.Hop, len(cfg))
	for i, h := range cfg {
		out[i] = sshclient.Hop{
			User:       h.User,
			Host:       h.Host,
			Port:       h.Port,
			AuthType:   h.AuthType,
			Password:   h.Password,
			KeyContent: h.KeyContent,
			Passphrase: h.Passphrase,
		}
	}
	return out
}
