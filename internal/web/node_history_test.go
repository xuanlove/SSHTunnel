package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sshtunnel/internal/logger"
)

// TestNodeHistoryRequiresTunnelID 缺少 tunnel_id 时返回 400
func TestNodeHistoryRequiresTunnelID(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	req := httptest.NewRequest(http.MethodGet, "/node/history", nil)
	rec := httptest.NewRecorder()
	h.handleNodeHistory(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, want 400", rec.Code)
	}
}

// TestNodeHistoryEmpty 无运行实例时返回空数组而非 null
func TestNodeHistoryEmpty(t *testing.T) {
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil)
	req := httptest.NewRequest(http.MethodGet, "/node/history?tunnel_id=abc&limit=100", nil)
	rec := httptest.NewRecorder()
	h.handleNodeHistory(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec.Code)
	}
	var resp struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if strings.TrimSpace(string(resp.Data)) != "[]" {
		t.Errorf("data = %s, want []", resp.Data)
	}
}
