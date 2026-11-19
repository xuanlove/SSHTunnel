package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	loginMaxFailures  = 5
	loginLockDuration = time.Minute
)

type loginAttempt struct {
	failures int
	until    time.Time
}

// loginLimiter 按来源 IP 限制登录失败次数，缓解在线爆破
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempt
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: make(map[string]*loginAttempt)}
}

// allow 判断该来源当前是否允许尝试登录
func (l *loginLimiter) allow(src string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[src]
	if !ok {
		return true
	}
	if a.until.IsZero() {
		return true
	}
	if a.until.After(time.Now()) {
		return false
	}
	// 锁定已过期：清除记录
	delete(l.attempts, src)
	return true
}

// fail 记录一次失败；达到阈值后锁定一段时间
func (l *loginLimiter) fail(src string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.attempts[src]
	if a == nil {
		a = &loginAttempt{}
		l.attempts[src] = a
	}
	a.failures++
	if a.failures >= loginMaxFailures {
		a.until = time.Now().Add(loginLockDuration)
		a.failures = 0
	}
}

// reset 登录成功后清除失败记录
func (l *loginLimiter) reset(src string) {
	l.mu.Lock()
	delete(l.attempts, src)
	l.mu.Unlock()
}

// loginSource 提取来源 IP（去掉端口）
func loginSource(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
