package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/logger"
	"sshtunnel/internal/proxy"
)

// seedAccessShard 在目录写入当日访问分片
func seedAccessShard(t *testing.T, dir string, recs []proxy.AccessRecord) {
	t.Helper()
	var b strings.Builder
	for _, r := range recs {
		j, _ := json.Marshal(r)
		b.Write(j)
		b.WriteByte('\n')
	}
	name := "access-" + time.Now().Format("20060102") + ".jsonl"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0600); err != nil {
		t.Fatalf("写入分片失败: %v", err)
	}
}

// TestHandleHistorySearch 检索接口返回持久化记录
func TestHandleHistorySearch(t *testing.T) {
	dir := t.TempDir()
	seedAccessShard(t, dir, []proxy.AccessRecord{
		{TunnelID: "t1", Result: "成功", Time: time.Now().Add(-time.Minute)},
		{TunnelID: "t2", Result: "拒绝: 防火墙", Denied: true, Time: time.Now()},
	})
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil).WithHistoryDir(dir)

	rec := httptest.NewRequest(http.MethodGet, "/history/search?tunnel_id=t1", nil)
	rec2 := httptest.NewRecorder()
	h.handleHistorySearch(rec2, rec)
	if rec2.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec2.Code)
	}
	var resp struct {
		Data []proxy.AccessRecord `json:"data"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].TunnelID != "t1" {
		t.Errorf("检索结果不符: %+v", resp.Data)
	}

	// 非法 from 时间返回 400
	bad := httptest.NewRequest(http.MethodGet, "/history/search?from=xxx", nil)
	badRec := httptest.NewRecorder()
	h.handleHistorySearch(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Errorf("非法时间应返回 400, got %d", badRec.Code)
	}
}

// TestHandleHistoryExport 导出接口返回 CSV 文本
func TestHandleHistoryExport(t *testing.T) {
	dir := t.TempDir()
	seedAccessShard(t, dir, []proxy.AccessRecord{
		{TunnelID: "t1", TunnelName: "节点A", Result: "成功", DurationMs: 42, Time: time.Now()},
	})
	h := NewHandler(nil, nil, logger.NewTest(), nil, "", "", nil).WithHistoryDir(dir)

	rec := httptest.NewRequest(http.MethodGet, "/history/export?tunnel_id=t1", nil)
	rec2 := httptest.NewRecorder()
	h.handleHistoryExport(rec2, rec)
	if rec2.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, want 200", rec2.Code)
	}
	var resp struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !strings.Contains(resp.Data, "time,tunnel,protocol") {
		t.Errorf("缺少 CSV 表头: %q", resp.Data)
	}
	if !strings.Contains(resp.Data, "节点A") || !strings.Contains(resp.Data, "42") {
		t.Errorf("CSV 内容不符: %q", resp.Data)
	}
}
