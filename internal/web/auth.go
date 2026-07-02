package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// authMiddleware 可选鉴权中间件
// authEnabled=false 时直接放行；true 时校验 JWT Token
func authMiddleware(authEnabled bool, jwtSecret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !authEnabled {
				next.ServeHTTP(w, r)
				return
			}

			// 提取 token
			token := extractToken(r)
			if token == "" {
				writeError(w, 401, "未授权：缺少 Token")
				return
			}

			// 校验 JWT
			claims, err := validateJWT(token, jwtSecret)
			if err != nil {
				writeError(w, 401, "Token 无效或已过期")
				return
			}
			_ = claims

			next.ServeHTTP(w, r)
		})
	}
}

// extractToken 从 Authorization Header 或 query 参数提取 token
func extractToken(r *http.Request) string {
	// Authorization: Bearer xxx
	authHeader := r.Header.Get("Authorization")
	if authHeader != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(authHeader, prefix) {
			return strings.TrimPrefix(authHeader, prefix)
		}
	}
	// query 参数（WebSocket 场景）
	return r.URL.Query().Get("token")
}

// generateJWT 签发 JWT Token
func generateJWT(username string, secret []byte) (string, int64, error) {
	expiresAt := time.Now().Add(24 * time.Hour).Unix()
	claims := jwt.MapClaims{
		"username":  username,
		"exp":       expiresAt,
		"iat":       time.Now().Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(secret)
	if err != nil {
		return "", 0, err
	}
	return signed, expiresAt, nil
}

// validateJWT 校验 JWT Token
func validateJWT(tokenString string, secret []byte) (jwt.Claims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrTokenSignatureInvalid
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	return token.Claims, nil
}

// ctxKey 上下文键类型，避免冲突
type ctxKey string

const (
	// 请求超时
	requestTimeout = 30 * time.Second
)

// withTimeout 为请求添加超时上下文
func withTimeout(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), requestTimeout)
}
