package proxy

import (
	"testing"
	"time"
)

// timeNow 便于测试阅读
func timeNow() time.Time { return time.Now() }

// TestAccessHistorySinkRestore 持久化钩子仅对新记录触发，恢复不触发。
// 恢复须在任何 Add 之前（对应启动时从分片恢复的真实顺序），保证环形槽位按时间有序。
func TestAccessHistorySinkRestore(t *testing.T) {
	h := NewAccessHistory(8)
	var sunk []AccessRecord
	h.SetSink(func(r AccessRecord) { sunk = append(sunk, r) })

	base := timeNow()
	// 恢复（升序）不触发钩子
	h.Restore([]AccessRecord{{TunnelID: "t0", Target: "old", Time: base.Add(-time.Minute)}})
	if len(sunk) != 0 {
		t.Fatalf("恢复不应触发钩子: %d", len(sunk))
	}

	h.Add(AccessRecord{TunnelID: "t1", Target: "a", Time: base})
	h.Add(AccessRecord{TunnelID: "t1", Target: "b", Time: base.Add(time.Second)})
	if len(sunk) != 2 {
		t.Fatalf("钩子触发次数 = %d, want 2", len(sunk))
	}

	recs := h.Recent(10)
	if len(recs) != 3 || recs[0].Target != "b" || recs[2].Target != "old" {
		t.Errorf("恢复后 Recent 不符: %+v", recs)
	}

	// nil 安全
	var nilHistory *AccessHistory
	nilHistory.SetSink(func(AccessRecord) {})
	nilHistory.Restore(nil)
	nilHistory.Add(AccessRecord{})
}
