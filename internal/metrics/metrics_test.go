package metrics

import (
	"testing"
	"time"
)

func TestStoreQueryDeltas(t *testing.T) {
	s := NewStore(time.Hour, 10*time.Second)
	base := time.Now().Add(-time.Minute)
	s.Add("t1", base, Raw{ActiveConns: 1, TotalConns: 10, BytesSent: 100, BytesRecv: 200})
	s.Add("t1", base.Add(10*time.Second), Raw{ActiveConns: 3, TotalConns: 12, BytesSent: 400, BytesRecv: 500})
	s.Add("t1", base.Add(20*time.Second), Raw{ActiveConns: 2, TotalConns: 13, BytesSent: 900, BytesRecv: 600})

	pts := s.Query("t1", base.Add(-time.Hour), 120)
	if len(pts) != 2 {
		t.Fatalf("点数 = %d, want 2", len(pts))
	}
	if pts[0].ActiveConns != 3 || pts[0].ConnDelta != 2 || pts[0].SentDelta != 300 || pts[0].RecvDelta != 300 {
		t.Errorf("第一个点不符: %+v", pts[0])
	}
	if pts[1].ActiveConns != 2 || pts[1].ConnDelta != 1 || pts[1].SentDelta != 500 || pts[1].RecvDelta != 100 {
		t.Errorf("第二个点不符: %+v", pts[1])
	}

	if got := s.Query("missing", base, 120); len(got) != 0 {
		t.Errorf("未知序列应返回空: %+v", got)
	}
}

func TestStoreDownsample(t *testing.T) {
	s := NewStore(time.Hour, time.Second)
	base := time.Now().Add(-time.Hour)
	totalConn, totalSent := int64(0), int64(0)
	for i := 0; i < 1000; i++ {
		totalConn += 2
		totalSent += 100
		s.Add("t1", base.Add(time.Duration(i)*time.Second), Raw{
			ActiveConns: int64(i % 50),
			TotalConns:  totalConn,
			BytesSent:   totalSent,
		})
	}

	pts := s.Query("t1", base, 10)
	if len(pts) == 0 || len(pts) > 10 {
		t.Fatalf("降采样点数 = %d, want 1..10", len(pts))
	}
	var connSum, sentSum int64
	for _, p := range pts {
		connSum += p.ConnDelta
		sentSum += p.SentDelta
	}
	if connSum != 1998 { // 999 个区间，每区间 +2
		t.Errorf("累计连接增量 = %d, want 1998", connSum)
	}
	if sentSum != 99900 {
		t.Errorf("累计上行增量 = %d, want 99900", sentSum)
	}
}

// TestQuerySinceBoundary since 之前的样本仅作为增量基准，不产生趋势点
func TestQuerySinceBoundary(t *testing.T) {
	s := NewStore(time.Hour, 10*time.Second)
	base := time.Now().Add(-time.Hour)
	s.Add("t1", base, Raw{TotalConns: 1})
	s.Add("t1", base.Add(10*time.Second), Raw{TotalConns: 2})
	since := base.Add(20 * time.Second)
	s.Add("t1", since, Raw{TotalConns: 3})
	s.Add("t1", since.Add(10*time.Second), Raw{TotalConns: 4})

	pts := s.Query("t1", since, 120)
	if len(pts) != 2 {
		t.Fatalf("点数 = %d, want 2", len(pts))
	}
	for _, p := range pts {
		if p.Time.Before(since) {
			t.Errorf("趋势点 %v 早于 since %v", p.Time, since)
		}
	}
	// 基准为 since 前最后样本（TotalConns=2）：两点增量均为 1
	if pts[0].ConnDelta != 1 || pts[1].ConnDelta != 1 {
		t.Errorf("增量不符: %+v", pts)
	}
}

// TestSnapshotRestoreRoundtrip 快照导出→恢复后查询结果一致，且旧样本被裁剪
func TestSnapshotRestoreRoundtrip(t *testing.T) {
	s := NewStore(time.Hour, 10*time.Second)
	base := time.Now().Add(-time.Minute)
	s.Add("t1", base, Raw{TotalConns: 10, BytesSent: 100})
	s.Add("t1", base.Add(10*time.Second), Raw{TotalConns: 12, BytesSent: 400})
	s.Add("t1", base.Add(20*time.Second), Raw{TotalConns: 13, BytesSent: 900})

	snap := s.Snapshot()
	if len(snap["t1"]) != 3 {
		t.Fatalf("快照样本数 = %d, want 3", len(snap["t1"]))
	}

	// 恢复到新 store
	s2 := NewStore(time.Hour, 10*time.Second)
	s2.RestoreAll(snap)
	pts := s2.Query("t1", base, 120)
	if len(pts) != 2 {
		t.Fatalf("恢复后点数 = %d, want 2", len(pts))
	}
	if pts[0].ConnDelta != 2 || pts[0].SentDelta != 300 || pts[1].SentDelta != 500 {
		t.Errorf("恢复后增量不符: %+v", pts)
	}

	// 过期序列不导出：清空后写入旧样本
	old := NewStore(time.Hour, 10*time.Second)
	old.Add("old", time.Now().Add(-2*time.Hour), Raw{TotalConns: 1})
	if got := old.Snapshot(); len(got) != 0 {
		t.Errorf("过期序列不应导出: %d", len(got))
	}
}

// TestQueryGapNoSpike 采样间隔过大（重启恢复边界）不产生虚假增量
func TestQueryGapNoSpike(t *testing.T) {
	s := NewStore(time.Hour, 10*time.Second)
	base := time.Now().Add(-time.Hour)
	s.Add("t1", base, Raw{TotalConns: 100, BytesSent: 1000})
	s.Add("t1", base.Add(10*time.Second), Raw{TotalConns: 110, BytesSent: 1500})
	// 重启恢复边界：间隔 1 小时，累计值继续增长
	s.Add("t1", base.Add(10*time.Second).Add(time.Hour), Raw{TotalConns: 200, BytesSent: 9000})
	s.Add("t1", base.Add(20*time.Second).Add(time.Hour), Raw{TotalConns: 205, BytesSent: 9500})

	pts := s.Query("t1", base, 120)
	if len(pts) != 3 {
		t.Fatalf("点数 = %d, want 3", len(pts))
	}
	if pts[0].ConnDelta != 10 || pts[0].SentDelta != 500 {
		t.Errorf("正常区间增量不符: %+v", pts[0])
	}
	if pts[1].ConnDelta != 0 || pts[1].SentDelta != 0 {
		t.Errorf("间隙点增量应为 0: %+v", pts[1])
	}
	if pts[2].ConnDelta != 5 || pts[2].SentDelta != 500 {
		t.Errorf("恢复后增量不符: %+v", pts[2])
	}
}

func TestParseRange(t *testing.T) {
	cases := map[string]time.Duration{
		"1h":  time.Hour,
		"6h":  6 * time.Hour,
		"24h": 24 * time.Hour,
		"x":   time.Hour,
	}
	for in, want := range cases {
		if got := ParseRange(in); got != want {
			t.Errorf("ParseRange(%q) = %v, want %v", in, got, want)
		}
	}
}
