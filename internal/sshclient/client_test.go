package sshclient

import (
	"testing"
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
			name:  "IPv6 地址（暂不支持，仅验证不崩溃）",
			input: "user@::1:22",
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
