package metrics

import (
	"context"
	"sort"
	"sync"
	"time"
)

// GlobalKey 全局汇总序列的键
const GlobalKey = "__global__"

// Raw 一次采样快照（累计值）
type Raw struct {
	ActiveConns int64
	TotalConns  int64
	BytesSent   int64
	BytesRecv   int64
}

// Sample 单条采样（累计值）
type Sample struct {
	Time        time.Time `json:"time"`
	ActiveConns int64     `json:"active_conns"`
	TotalConns  int64     `json:"total_conns"`
	BytesSent   int64     `json:"bytes_sent"`
	BytesRecv   int64     `json:"bytes_recv"`
}

// Point 降采样后的趋势点（区间增量）
type Point struct {
	Time        time.Time `json:"time"`
	ActiveConns int64     `json:"active_conns"`
	ConnDelta   int64     `json:"conn_delta"`
	SentDelta   int64     `json:"sent_delta"`
	RecvDelta   int64     `json:"recv_delta"`
}

// ring 固定容量环形缓冲
type ring struct {
	buf  []Sample
	next int
	full bool
}

func (r *ring) push(s Sample) {
	if len(r.buf) < cap(r.buf) {
		r.buf = append(r.buf, s)
		return
	}
	r.buf[r.next] = s
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// Store 内存指标采样存储（按 key 保留固定时长的样本）
type Store struct {
	mu       sync.RWMutex
	interval time.Duration
	capacity int
	series   map[string]*ring
}

// NewStore 创建采样存储；retention 为保留时长，interval 为采样间隔
func NewStore(retention, interval time.Duration) *Store {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	capacity := int(retention / interval)
	if capacity < 2 {
		capacity = 2
	}
	return &Store{
		interval: interval,
		capacity: capacity,
		series:   make(map[string]*ring),
	}
}

// Interval 返回采样间隔
func (s *Store) Interval() time.Duration {
	return s.interval
}

// Add 追加一次采样
func (s *Store) Add(key string, ts time.Time, raw Raw) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.series[key]
	if r == nil {
		r = &ring{buf: make([]Sample, 0, s.capacity)}
		s.series[key] = r
	}
	r.push(Sample{
		Time:        ts,
		ActiveConns: raw.ActiveConns,
		TotalConns:  raw.TotalConns,
		BytesSent:   raw.BytesSent,
		BytesRecv:   raw.BytesRecv,
	})
}

// Run 周期采样；snapshot 返回各序列的累计值。ctx 取消后停止采样
func (s *Store) Run(ctx context.Context, snapshot func() map[string]Raw) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case ts := <-ticker.C:
			for key, raw := range snapshot() {
				s.Add(key, ts, raw)
			}
		}
	}
}

// Query 返回 since 之后的趋势点（按时间升序，最多 maxPoints 个）。
// 以 since 前最近一个样本为增量基准，返回的点均在 since 之后。
func (s *Store) Query(key string, since time.Time, maxPoints int) []Point {
	if maxPoints <= 0 {
		maxPoints = 120
	}
	s.mu.RLock()
	r := s.series[key]
	var samples []Sample
	if r != nil {
		samples = make([]Sample, len(r.buf))
		copy(samples, r.buf)
	}
	s.mu.RUnlock()
	if len(samples) < 2 {
		return []Point{}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].Time.Before(samples[j].Time) })

	start := 0
	for i := 0; i < len(samples); i++ {
		if samples[i].Time.Before(since) {
			start = i
		} else {
			break
		}
	}

	type deltaPoint struct {
		sample Sample
		conn   int64
		sent   int64
		recv   int64
	}
	pts := make([]deltaPoint, 0, len(samples))
	for i := start + 1; i < len(samples); i++ {
		prev, cur := samples[i-1], samples[i]
		pts = append(pts, deltaPoint{
			sample: cur,
			conn:   clampDelta(cur.TotalConns - prev.TotalConns),
			sent:   clampDelta(cur.BytesSent - prev.BytesSent),
			recv:   clampDelta(cur.BytesRecv - prev.BytesRecv),
		})
	}
	if len(pts) == 0 {
		return []Point{}
	}
	if len(pts) <= maxPoints {
		out := make([]Point, 0, len(pts))
		for _, p := range pts {
			out = append(out, Point{
				Time:        p.sample.Time,
				ActiveConns: p.sample.ActiveConns,
				ConnDelta:   p.conn,
				SentDelta:   p.sent,
				RecvDelta:   p.recv,
			})
		}
		return out
	}

	// 分桶降采样：活动连接取最大值，流量与连接数取区间求和
	span := pts[len(pts)-1].sample.Time.Sub(pts[0].sample.Time)
	bucketDur := span / time.Duration(maxPoints)
	if bucketDur <= 0 {
		bucketDur = time.Second
	}
	buckets := make([]Point, 0, maxPoints)
	for _, p := range pts {
		idx := int(p.sample.Time.Sub(pts[0].sample.Time) / bucketDur)
		if idx >= maxPoints {
			idx = maxPoints - 1
		}
		for len(buckets) <= idx {
			// 空洞补位使用预期桶起始时间，保持时间轴单调
			buckets = append(buckets, Point{Time: pts[0].sample.Time.Add(time.Duration(len(buckets)) * bucketDur)})
		}
		b := &buckets[idx]
		if p.sample.ActiveConns > b.ActiveConns {
			b.ActiveConns = p.sample.ActiveConns
		}
		b.ConnDelta += p.conn
		b.SentDelta += p.sent
		b.RecvDelta += p.recv
	}
	return buckets
}

func clampDelta(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// ParseRange 将范围键（1h/6h/24h）解析为时长
func ParseRange(key string) time.Duration {
	switch key {
	case "6h":
		return 6 * time.Hour
	case "24h":
		return 24 * time.Hour
	default:
		return time.Hour
	}
}
