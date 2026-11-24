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

// TestLoginLimiterExpiresUnlockedRecords 未达锁定阈值的失败记录也按 lockDuration 过期清理，map 有界
func TestLoginLimiterExpiresUnlockedRecords(t *testing.T) {
	l := newLoginLimiter()

	// 多个来源各记录 1 次失败（未达锁定阈值）
	srcs := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"}
	for _, src := range srcs {
		l.fail(src)
	}
	if len(l.attempts) != len(srcs) {
		t.Fatalf("失败后应保留 %d 条记录，实际 %d", len(srcs), len(l.attempts))
	}

	// 将失败时间拨回超过 lockDuration 之前
	l.mu.Lock()
	for _, a := range l.attempts {
		a.lastFail = time.Now().Add(-2 * loginLockDuration)
	}
	l.mu.Unlock()

	// 任意一次访问都应触发过期清理（allow 不新建记录，清理后 map 为空）
	l.allow("198.51.100.9")
	if len(l.attempts) != 0 {
		t.Errorf("过期记录应被清理，实际剩余 %d 条", len(l.attempts))
	}
	for _, src := range srcs {
		if _, ok := l.attempts[src]; ok {
			t.Errorf("来源 %s 的过期记录应被删除", src)
		}
	}

	// 清理后新来源仍可正常记录与锁定
	const fresh = "198.51.100.9"
	for i := 0; i < loginMaxFailures; i++ {
		l.fail(fresh)
	}
	if l.allow(fresh) {
		t.Errorf("清理后新来源失败 %d 次仍应被锁定", loginMaxFailures)
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
