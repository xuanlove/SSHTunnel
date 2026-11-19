package web

import (
	"net/http/httptest"
	"testing"
	"time"
)

// TestLoginLimiterLocksAfterFailures 连续失败达到阈值后锁定，成功后重置
func TestLoginLimiterLocksAfterFailures(t *testing.T) {
	l := newLoginLimiter()
	const src = "203.0.113.10"

	for i := 0; i < loginMaxFailures; i++ {
		if !l.allow(src) {
			t.Fatalf("第 %d 次尝试前不应被锁定", i+1)
		}
		l.fail(src)
	}
	if l.allow(src) {
		t.Fatalf("连续失败 %d 次后应被锁定", loginMaxFailures)
	}

	// 锁定过期后恢复
	l.mu.Lock()
	l.attempts[src].until = time.Now().Add(-time.Second)
	l.mu.Unlock()
	if !l.allow(src) {
		t.Errorf("锁定过期后应允许重试")
	}

	// 成功登录后清除失败记录
	l.fail(src)
	l.reset(src)
	if !l.allow(src) {
		t.Errorf("成功登录后应清除失败记录")
	}
}

// TestLoginSource 来源 IP 提取（去掉端口）
func TestLoginSource(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = "192.0.2.5:54321"
	if got := loginSource(r); got != "192.0.2.5" {
		t.Errorf("loginSource = %q, want 192.0.2.5", got)
	}
}
