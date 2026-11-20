package persist

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sshtunnel/internal/proxy"
)

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// TestWriterDailyShard 入队记录后应按天分片落盘
func TestWriterDailyShard(t *testing.T) {
	dir := t.TempDir()
	w := NewWriter(dir, "access-", 0)
	defer w.Close()

	w.Enqueue(proxy.AccessRecord{TunnelID: "t1", Result: "成功"})
	waitFor(t, 3*time.Second, func() bool {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "access-") && strings.HasSuffix(e.Name(), ".jsonl") {
				return true
			}
		}
		return false
	})

	recs, err := ReadJSONLDesc[proxy.AccessRecord](dir, "access-", 0)
	if err != nil {
		t.Fatalf("读取分片失败: %v", err)
	}
	if len(recs) != 1 || recs[0].TunnelID != "t1" {
		t.Errorf("恢复记录不符: %+v", recs)
	}
}

// TestWriterRetention 超过保留期的分片应被清理
func TestWriterRetention(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "access-20200101.jsonl")
	if err := os.WriteFile(old, []byte(`{"tunnel_id":"old"}`+"\n"), 0600); err != nil {
		t.Fatalf("写入旧分片失败: %v", err)
	}
	w := NewWriter(dir, "access-", 24*time.Hour)
	defer w.Close()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("过期分片应被清理")
	}
}

// TestWriterCloseEnqueue Close 后入队应被忽略且不 panic
func TestWriterCloseEnqueue(t *testing.T) {
	w := NewWriter(t.TempDir(), "x-", 0)
	w.Close()
	w.Enqueue(proxy.AccessRecord{}) // 不应 panic
}

// TestSearchAccess 检索过滤：隧道/时间/结果/分页
func TestSearchAccess(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	recs := []proxy.AccessRecord{
		{Time: base, TunnelID: "t1", Result: "成功"},
		{Time: base.Add(time.Minute), TunnelID: "t1", Result: "失败: 连接目标失败"},
		{Time: base.Add(2 * time.Minute), TunnelID: "t2", Result: "成功"},
		{Time: base.Add(3 * time.Minute), TunnelID: "t1", Result: "拒绝: 防火墙", Denied: true},
	}
	var b strings.Builder
	for _, r := range recs {
		j, _ := json.Marshal(r)
		b.Write(j)
		b.WriteByte('\n')
	}
	shard := filepath.Join(dir, "access-"+base.Format("20060102")+".jsonl")
	if err := os.WriteFile(shard, []byte(b.String()), 0600); err != nil {
		t.Fatalf("写入分片失败: %v", err)
	}

	// 全部（倒序）
	got, err := SearchAccess(dir, AccessFilter{})
	if err != nil || len(got) != 4 {
		t.Fatalf("全部检索 = %d 条, err=%v, want 4", len(got), err)
	}
	if got[0].Result != "拒绝: 防火墙" {
		t.Errorf("应时间倒序: %+v", got[0])
	}

	// 隧道过滤
	if got, _ := SearchAccess(dir, AccessFilter{TunnelID: "t1"}); len(got) != 3 {
		t.Errorf("t1 检索 = %d 条, want 3", len(got))
	}

	// 结果过滤
	if got, _ := SearchAccess(dir, AccessFilter{Result: "denied"}); len(got) != 1 {
		t.Errorf("denied 检索 = %d 条, want 1", len(got))
	}
	if got, _ := SearchAccess(dir, AccessFilter{Result: "success"}); len(got) != 2 {
		t.Errorf("success 检索 = %d 条, want 2", len(got))
	}
	if got, _ := SearchAccess(dir, AccessFilter{Result: "failed"}); len(got) != 1 {
		t.Errorf("failed 检索 = %d 条, want 1", len(got))
	}

	// 时间范围
	from := base.Add(30 * time.Second)
	if got, _ := SearchAccess(dir, AccessFilter{From: from}); len(got) != 3 {
		t.Errorf("from 过滤 = %d 条, want 3", len(got))
	}

	// 分页
	if got, _ := SearchAccess(dir, AccessFilter{Limit: 2}); len(got) != 2 || got[0].Result != "拒绝: 防火墙" {
		t.Errorf("limit 分页不符: %+v", got)
	}
	if got, _ := SearchAccess(dir, AccessFilter{Limit: 2, Offset: 2}); len(got) != 2 || got[0].Result != "失败: 连接目标失败" {
		t.Errorf("offset 分页不符: %+v", got)
	}
	if got, _ := SearchAccess(dir, AccessFilter{Limit: 2, Offset: 99}); len(got) != 0 {
		t.Errorf("越界 offset 应为空: %+v", got)
	}
}

// TestAccessCSV CSV 渲染与特殊字符转义
func TestAccessCSV(t *testing.T) {
	recs := []proxy.AccessRecord{{
		Time:       time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local),
		TunnelName: "a,b",
		Result:     `说"不"`,
		DurationMs: 42,
	}}
	csv := AccessCSV(recs)
	lines := strings.Split(strings.TrimRight(csv, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("行数 = %d, want 2", len(lines))
	}
	if !strings.Contains(lines[1], `"a,b"`) || !strings.Contains(lines[1], `"说""不"""`) {
		t.Errorf("CSV 转义不符: %s", lines[1])
	}
	if !strings.Contains(lines[1], "2026-01-02 03:04:05") {
		t.Errorf("时间格式不符: %s", lines[1])
	}
}

// TestWriteFileAtomic 原子写入内容正确且权限 0600
func TestWriteFileAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	if err := WriteFileAtomic(path, []byte("hello")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "hello" {
		t.Fatalf("内容不符: %v %q", err, b)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat 失败: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0600 {
		t.Errorf("权限 = %o, want 600", perm)
	}
}
