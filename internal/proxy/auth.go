package proxy

import (
	"sshsuidao/internal/config"
)

// authHelper 认证辅助，统一处理各协议认证
type authHelper struct {
	auth *config.AuthConfig
}

// hasAuth 是否启用认证
func (a *authHelper) hasAuth() bool {
	return a.auth != nil && a.auth.Username != ""
}

// checkBasicAuth HTTP Basic Auth 校验（返回 base64 解码后的 user:pass）
func (a *authHelper) checkBasicAuth(user, pass string) bool {
	if !a.hasAuth() {
		return true
	}
	return a.auth.Username == user && a.auth.Password == pass
}

// checkUserID SOCKS4 UserID 校验（仅匹配用户名）
func (a *authHelper) checkUserID(userID string) bool {
	if !a.hasAuth() {
		return true
	}
	return a.auth.Username == userID
}

// checkSocks5 SOCKS5 用户名密码校验
func (a *authHelper) checkSocks5(user, pass string) bool {
	if !a.hasAuth() {
		return true
	}
	return a.auth.Username == user && a.auth.Password == pass
}
