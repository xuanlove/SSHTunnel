package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sshtunnel/internal/logger"
)

// newTestMux 构建带嵌入式前端资源的路由树
func newTestMux(t *testing.T) http.Handler {
	t.Helper()
	srv := NewServer(Config{}, NewHandler(nil, nil, logger.NewTest(), []byte("secret"), "", "", nil))
	return srv.buildRoutes()
}

// TestStaticIndexNoCache 验证入口 HTML 禁止缓存（升级后能拿到新资源引用）
func TestStaticIndexNoCache(t *testing.T) {
	mux := newTestMux(t)
	for _, path := range []string{"/", "/logs", "/settings"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != http.StatusOK {
			t.Errorf("%s 期望 200，实际 %d", path, w.Code)
			continue
		}
		if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
			t.Errorf("%s 的 Cache-Control 应含 no-cache，实际 %q", path, cc)
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("%s 应返回 HTML，实际 %q", path, ct)
		}
	}
}

// TestStaticMissingAsset404 验证缺失的静态资源返回 404 而非回退 HTML。
// 回归：升级后浏览器仍引用旧 hash 的 chunk 时，若回退成 HTML 会导致
// 动态导入失败，前端路由导航被中止（表现为点击菜单无反应）。
func TestStaticMissingAsset404(t *testing.T) {
	mux := newTestMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/assets/Logs-OLDHASH123.js", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("缺失资源期望 404，实际 %d（响应体: %s）", w.Code, w.Body.String())
	}
}

// TestStaticAssetLongCache 验证带 hash 的资源使用长缓存
func TestStaticAssetLongCache(t *testing.T) {
	mux := newTestMux(t)
	// 先从 index.html 解析出真实 chunk 路径
	idx := httptest.NewRecorder()
	mux.ServeHTTP(idx, httptest.NewRequest("GET", "/", nil))
	body := idx.Body.String()
	start := strings.Index(body, "/assets/")
	if start < 0 {
		t.Skip("index.html 中未找到 assets 引用")
	}
	rest := body[start:]
	end := strings.IndexAny(rest, "\"'")
	if end < 0 {
		t.Skip("无法解析 chunk 路径")
	}
	asset := rest[:end]

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", asset, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s 期望 200，实际 %d", asset, w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("%s 的 Cache-Control 应含 immutable，实际 %q", asset, cc)
	}
}
