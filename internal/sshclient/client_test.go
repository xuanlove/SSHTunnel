package sshclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestParseHopChain(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []Hop
	}{
		{
			name:  "单跳带端口",
			input: "user@host:2222",
			want:  []Hop{{User: "user", Host: "host", Port: 2222}},
		},
		{
			name:  "单跳默认端口",
			input: "user@host",
			want:  []Hop{{User: "user", Host: "host", Port: 22}},
		},
		{
			name:  "仅主机",
			input: "host.example.com",
			want:  []Hop{{User: "", Host: "host.example.com", Port: 22}},
		},
		{
			name:  "多跳",
			input: "u1@h1:22,u2@h2:2222,u3@h3",
			want: []Hop{
				{User: "u1", Host: "h1", Port: 22},
				{User: "u2", Host: "h2", Port: 2222},
				{User: "u3", Host: "h3", Port: 22},
			},
		},
		{
			name:  "带空格的多跳",
			input: " u1@h1:22 , u2@h2:2222 ",
			want: []Hop{
				{User: "u1", Host: "h1", Port: 22},
				{User: "u2", Host: "h2", Port: 2222},
			},
		},
		{
			name:  "箭头分隔多跳",
			input: "user@host1:22 -> admin@host2:2222",
			want: []Hop{
				{User: "user", Host: "host1", Port: 22},
				{User: "admin", Host: "host2", Port: 2222},
			},
		},
		{
			name:  "箭头分隔三跳带空格",
			input: "u1@h1:22 -> u2@h2:2222 -> u3@h3",
			want: []Hop{
				{User: "u1", Host: "h1", Port: 22},
				{User: "u2", Host: "h2", Port: 2222},
				{User: "u3", Host: "h3", Port: 22},
			},
		},
		{
			name:  "混合分隔符",
			input: "u1@h1:22, u2@h2:2222 -> u3@h3",
			want: []Hop{
				{User: "u1", Host: "h1", Port: 22},
				{User: "u2", Host: "h2", Port: 2222},
				{User: "u3", Host: "h3", Port: 22},
			},
		},
		{
			name:  "空字符串",
			input: "",
			want:  nil,
		},
		{
			name:  "仅逗号",
			input: ",,",
			want:  nil,
		},
		{
			name:  "IPv6 方括号与端口",
			input: "user@[::1]:2222",
			want:  []Hop{{User: "user", Host: "::1", Port: 2222}},
		},
		{
			name:  "IPv6 无端口",
			input: "user@::1",
			want:  []Hop{{User: "user", Host: "::1", Port: 22}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseHopChain(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("长度不匹配: got %d, want %d (got=%v)", len(got), len(tt.want), got)
			}
			for i, h := range got {
				w := tt.want[i]
				if h.User != w.User || h.Host != w.Host || h.Port != w.Port {
					t.Errorf("第 %d 跳不匹配: got %+v, want %+v", i+1, h, w)
				}
			}
		})
	}
}

// genEd25519 在内存生成 ed25519 密钥对并转为 SSH 公钥（模拟 server 端主机公钥）
func genEd25519(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成 ed25519 密钥失败: %v", err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("构造 SSH 公钥失败: %v", err)
	}
	return pub
}

func TestBuildAuthPasswordIncludesKeyboardInteractive(t *testing.T) {
	methods, err := buildAuth(Hop{AuthType: "password", Password: "pw"})
	if err != nil {
		t.Fatalf("密码模式构造认证失败: %v", err)
	}
	// 密码模式应同时提供 Password 与 KeyboardInteractive（部分服务器禁用密码认证
	// 但保留键盘交互认证，缺失会导致 unable to authenticate）
	if len(methods) != 2 {
		t.Fatalf("密码模式应提供 2 种认证方法（Password+KeyboardInteractive），实际 %d", len(methods))
	}

	keyMethods, err := buildAuth(Hop{AuthType: "key", KeyContent: "not-a-pem"})
	if err == nil {
		t.Fatalf("非法 PEM 应返回解析错误")
	}
	if keyMethods != nil {
		t.Fatalf("解析失败时不应返回认证方法")
	}
}

func TestNormalizeHostKey(t *testing.T) {
	pub := genEd25519(t)
	blob := pub.Marshal()
	blobB64 := base64.RawStdEncoding.EncodeToString(blob)
	wantFP := fingerprintKey(pub)

	authorizedLine := pub.Type() + " " + blobB64 + " user@host"

	// 三种输入均应规范化为同一指纹
	for _, tt := range []struct {
		name  string
		input string
	}{
		{"SHA256 指纹", wantFP},
		{"base64 公钥裸串", blobB64},
		{"authorized_keys 行", authorizedLine},
		{"带空白与 padding 的指纹", "  " + wantFP + "=  "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeHostKey(tt.input)
			if err != nil {
				t.Fatalf("规范化失败: %v", err)
			}
			if got != wantFP {
				t.Errorf("指纹不符: got %s, want %s", got, wantFP)
			}
		})
	}

	// 空输入表示未配置（可选），返回空串且不报错
	if got, err := NormalizeHostKey(""); err != nil || got != "" {
		t.Errorf("空 host_key 应视为未配置: got=%q err=%v", got, err)
	}

	// 非法输入应报错
	for _, bad := range []string{"SHA256:", "not base64!!!"} {
		if _, err := NormalizeHostKey(bad); err == nil {
			t.Errorf("非法输入 %q 应报错", bad)
		}
	}
}

func TestHostKeyCallback(t *testing.T) {
	pub := genEd25519(t)
	other := genEd25519(t)
	fp := fingerprintKey(pub)
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22}

	// 匹配：指纹一致放行（authorized_keys 行形式同样应通过）
	for _, hostKey := range []string{fp, pub.Type() + " " + base64.RawStdEncoding.EncodeToString(pub.Marshal()) + " c"} {
		expected, err := NormalizeHostKey(hostKey)
		if err != nil {
			t.Fatal(err)
		}
		cb := hostKeyCallback(expected)
		if err := cb("srv.example.com", addr, pub); err != nil {
			t.Errorf("密钥匹配应放行, host_key=%q: %v", hostKey, err)
		}
	}

	// 不匹配：另一把密钥必须拒绝，且错误信息包含期望/实际指纹
	cb := hostKeyCallback(fp)
	err := cb("srv.example.com", addr, other)
	if err == nil {
		t.Fatal("密钥不匹配应拒绝连接")
	}
	if !strings.Contains(err.Error(), "主机密钥校验失败") || !strings.Contains(err.Error(), fp) {
		t.Errorf("错误信息应包含明确原因: %v", err)
	}

	// 未配置 host_key（可选）时跳过校验：任意密钥均放行
	if err := hostKeyCallback("")("srv.example.com", addr, other); err != nil {
		t.Errorf("未配置 host_key 时应跳过校验: %v", err)
	}

	// 非法（非空）主机密钥在发起连接前即被拒绝
	if _, err := Dial([]Hop{{Host: "example.com", HostKey: "!!!"}}); err == nil || !strings.Contains(err.Error(), "主机密钥配置无效") {
		t.Fatalf("非法 host_key 应拒绝连接: %v", err)
	}
}

func TestConnectHopHonorsDeadline(t *testing.T) {
	conn, peer := net.Pipe()
	t.Cleanup(func() { _ = conn.Close(); _ = peer.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := connectHop(ctx, conn, nil, "localhost:22", &ssh.ClientConfig{
		HostKeyCallback: hostKeyCallback(fingerprintKey(genEd25519(t))),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("取消应中断握手，实际错误为 %v", err)
	}
}
