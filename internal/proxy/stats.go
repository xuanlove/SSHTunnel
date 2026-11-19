package proxy

import (
	"io"
	"net"
	"sync/atomic"
)

// TrafficStats 并发安全的连接数与流量计数。
// 可在代理监听器与本地端口转发之间复用。
type TrafficStats struct {
	active atomic.Int64 // 当前活跃连接数
	total  atomic.Int64 // 累计连接数
	sent   atomic.Int64 // 上行字节（客户端 -> 目标）
	recv   atomic.Int64 // 下行字节（目标 -> 客户端）
}

// BeginConn 记录一个连接建立
func (s *TrafficStats) BeginConn() {
	s.active.Add(1)
	s.total.Add(1)
}

// EndConn 记录一个连接结束
func (s *TrafficStats) EndConn() {
	s.active.Add(-1)
}

// AddSent 累加上行字节数
func (s *TrafficStats) AddSent(n int64) {
	if n > 0 {
		s.sent.Add(n)
	}
}

// AddRecv 累加下行字节数
func (s *TrafficStats) AddRecv(n int64) {
	if n > 0 {
		s.recv.Add(n)
	}
}

// Snapshot 读取当前计数快照
func (s *TrafficStats) Snapshot() (active, total, sent, recv int64) {
	return s.active.Load(), s.total.Load(), s.sent.Load(), s.recv.Load()
}

// ListenerStats 单个代理监听器的运行统计（供界面展示）
type ListenerStats struct {
	ID          string `json:"id"`
	Protocol    string `json:"protocol"`
	ListenPort  int    `json:"listen_port"`
	ActiveConns int64  `json:"active_conns"`
	TotalConns  int64  `json:"total_conns"`
	BytesSent   int64  `json:"bytes_sent"` // 上行（客户端 -> 目标）
	BytesRecv   int64  `json:"bytes_recv"` // 下行（目标 -> 客户端）
}

// countWriter 写入时累计字节数（sent=true 计上行，否则计下行）
type countWriter struct {
	w     io.Writer
	stats *TrafficStats
	sent  bool
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if c.sent {
		c.stats.AddSent(int64(n))
	} else {
		c.stats.AddRecv(int64(n))
	}
	return n, err
}

// BridgeStats 双向桥接并按方向统计流量（按写入字节实时累计）。
// a 为客户端侧连接，b 为隧道目标侧连接；stats 可为 nil（仅桥接）。
func BridgeStats(a, b net.Conn, stats *TrafficStats) {
	aw, bw := io.Writer(a), io.Writer(b)
	if stats != nil {
		aw = &countWriter{w: a, stats: stats}             // 目标 -> 客户端 = 下行
		bw = &countWriter{w: b, stats: stats, sent: true} // 客户端 -> 目标 = 上行
	}
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(aw, b) // 目标 -> 客户端
		_ = a.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(bw, a) // 客户端 -> 目标
		_ = b.Close()
		done <- struct{}{}
	}()
	<-done
}
