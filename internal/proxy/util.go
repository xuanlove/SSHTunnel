package proxy

import (
	"net"
	"strings"
)

// Bridge 双向桥接两个连接（不统计流量）
func Bridge(a, b net.Conn) {
	BridgeStats(a, b, nil, nil)
}

// sanitizeLog 去除字符串中的 \r \n 及其他 ASCII 控制字符（含 DEL），
// 防止客户端可控字段注入伪造日志行
func sanitizeLog(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
