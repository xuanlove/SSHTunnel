package proxy

import (
	"net"
)

// Bridge 双向桥接两个连接（不统计流量）
func Bridge(a, b net.Conn) {
	BridgeStats(a, b, nil, nil)
}
