package proxy

import "testing"

// TestRecentByTunnel 按隧道过滤访问历史，保持时间倒序并遵守 limit
func TestRecentByTunnel(t *testing.T) {
	h := NewAccessHistory(8)
	for i := 0; i < 6; i++ {
		tid := "a"
		if i%2 == 1 {
			tid = "b"
		}
		h.Add(AccessRecord{TunnelID: tid, Target: string(rune('0' + i))})
	}

	recs := h.RecentByTunnel("a", 10)
	if len(recs) != 3 {
		t.Fatalf("a 隧道记录数 = %d, want 3: %+v", len(recs), recs)
	}
	for _, r := range recs {
		if r.TunnelID != "a" {
			t.Errorf("混入其他隧道记录: %+v", r)
		}
	}
	if recs[0].Target != "4" || recs[1].Target != "2" || recs[2].Target != "0" {
		t.Errorf("应按时间倒序: %+v", recs)
	}

	if got := h.RecentByTunnel("a", 2); len(got) != 2 {
		t.Errorf("limit 未生效: %d", len(got))
	}
	if got := h.RecentByTunnel("missing", 10); len(got) != 0 {
		t.Errorf("未知隧道应返回空: %+v", got)
	}

	var nilHistory *AccessHistory
	if got := nilHistory.RecentByTunnel("a", 10); len(got) != 0 {
		t.Errorf("nil 历史应返回空: %+v", got)
	}
}
