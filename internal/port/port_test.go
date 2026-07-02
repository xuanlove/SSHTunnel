package port

import (
	"testing"
)

func TestIsAvailable(t *testing.T) {
	// 测试可用端口（取一个不太可能被占用的端口）
	port := 19999
	if !IsAvailable(port) {
		t.Errorf("端口 %d 应该可用", port)
	}
}

func TestBindAddress(t *testing.T) {
	tests := []struct {
		port          int
		allowExternal bool
		want          string
	}{
		{1080, false, "127.0.0.1:1080"},
		{1080, true, "0.0.0.0:1080"},
		{8080, false, "127.0.0.1:8080"},
		{8080, true, "0.0.0.0:8080"},
	}
	for _, tt := range tests {
		got := BindAddress(tt.port, tt.allowExternal)
		if got != tt.want {
			t.Errorf("BindAddress(%d, %v) = %q, want %q", tt.port, tt.allowExternal, got, tt.want)
		}
	}
}

func TestFindAvailable(t *testing.T) {
	// 从一个较高端口开始，应该能找到可用端口
	p := FindAvailable(20000)
	if p == 0 {
		t.Errorf("FindAvailable(20000) 应返回非零端口")
	}
	if !IsAvailable(p) {
		t.Errorf("FindAvailable 返回的端口 %d 应该可用", p)
	}
}
