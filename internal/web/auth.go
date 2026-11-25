package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// authMiddleware 可选鉴权中间件（支持运行时动态开关）
// 使用当前认证状态和签名密钥校验请求，并附加会话生命周期。
func authMiddleware(state func() (bool, []byte, context.Context)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			enabled, jwtSecret, session := state()
			if session.Err() != nil {
				writeError(w, 401, "会话已失效")
				return
			}
			if !enabled {
				next.ServeHTTP(w, withAuthSession(r, session, time.Time{}))
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
			if err != nil || session.Err() != nil {
				writeError(w, 401, "Token 无效或已过期")
				return
			}
			expiresAt, _ := claims.GetExpirationTime()
			next.ServeHTTP(w, withAuthSession(r, session, expiresAt.Time))
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
		"username": username,
		"exp":      expiresAt,
		"iat":      time.Now().Unix(),
		"session":  "v2",
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
		return secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired())
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || claims["session"] != "v2" {
		return nil, jwt.ErrTokenInvalidClaims
	}
	return claims, nil
}

type authSessionKey struct{}

type authSession struct {
	ctx       context.Context
	expiresAt time.Time
}

func withAuthSession(r *http.Request, session context.Context, expiresAt time.Time) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authSessionKey{}, authSession{session, expiresAt}))
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
