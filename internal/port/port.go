package port

import (
	"fmt"
	"net"
)

// IsAvailable 检查端口是否可用
func IsAvailable(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// BindAddress 根据是否允许外部访问返回绑定地址
func BindAddress(port int, allowExternal bool) string {
	if allowExternal {
		return fmt.Sprintf("0.0.0.0:%d", port)
	}
	return fmt.Sprintf("127.0.0.1:%d", port)
}

// FindAvailable 从指定端口开始查找可用端口
func FindAvailable(start int) int {
	for p := start; p < start+100; p++ {
		if IsAvailable(p) {
			return p
		}
	}
	return 0
}
