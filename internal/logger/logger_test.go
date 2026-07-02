package logger

import (
	"strings"
	"testing"
	"time"
)

func TestLoggerBasic(t *testing.T) {
	// 使用 SetOutput 重定向避免污染真实日志文件
	log, err := New()
	if err != nil {
		t.Fatalf("创建 Logger 失败: %v", err)
	}
	defer log.Close()

	log.Info("test info message")
	log.Errorf("formatted %s %d", "msg", 42)
	log.TunnelInfo("tunnel-1", "tunnel message")

	// 应能获取最近日志
	recent := log.Recent(10)
	if len(recent) < 3 {
		t.Errorf("应至少有 3 条日志, got %d", len(recent))
	}

	// 验证最近一条是 tunnel message
	last := recent[len(recent)-1]
	if !strings.Contains(last.Message, "tunnel message") {
		t.Errorf("最后一条日志内容不匹配: %q", last.Message)
	}

	// 验证 formatted 日志存在
	foundFmt := false
	for _, e := range recent {
		if strings.Contains(e.Message, "formatted msg 42") {
			foundFmt = true
			break
		}
	}
	if !foundFmt {
		t.Errorf("未找到 formatted 日志")
	}
}

func TestLoggerSink(t *testing.T) {
	log, _ := New()
	defer log.Close()

	done := make(chan Entry, 1)
	log.AddSink(func(e Entry) {
		select {
		case done <- e:
		default:
		}
	})

	log.Info("sink-test")

	select {
	case e := <-done:
		if e.Message != "sink-test" {
			t.Errorf("sink 接收的消息不匹配: got %q", e.Message)
		}
	case <-time.After(500 * time.Millisecond):
		t.Errorf("sink 未接收到消息（超时）")
	}
}

func TestLoggerTunnelScoped(t *testing.T) {
	log, _ := New()
	defer log.Close()

	log.TunnelWarn("t-1", "warn msg")

	recent := log.Recent(100)
	found := false
	for _, e := range recent {
		if e.TunnelID == "t-1" && e.Message == "warn msg" && e.Level == "WARN" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("未找到带 TunnelID 的日志")
	}
}

func TestLoggerRingBuffer(t *testing.T) {
	log, _ := New()
	defer log.Close()
	// 缩小容量便于测试
	log.mu.Lock()
	log.ringCap = 5
	log.ring = make([]Entry, 0, 5)
	log.ringIdx = 0
	log.mu.Unlock()

	for i := 0; i < 10; i++ {
		log.Infof("msg-%d", i)
	}

	recent := log.Recent(100)
	if len(recent) != 5 {
		t.Errorf("环形缓冲应保留 5 条, got %d", len(recent))
	}
	// 应包含最后 5 条
	if !strings.Contains(recent[len(recent)-1].Message, "msg-9") {
		t.Errorf("最后一条应为 msg-9, got %q", recent[len(recent)-1].Message)
	}
}
