package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Level 日志级别
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "DEBUG"
	case LevelInfo:
		return "INFO"
	case LevelWarn:
		return "WARN"
	case LevelError:
		return "ERROR"
	default:
		return "UNKNOWN"
	}
}

// Entry 日志条目
type Entry struct {
	Time     time.Time `json:"time"`
	Level    string    `json:"level"`
	Message  string    `json:"message"`
	TunnelID string    `json:"tunnel_id,omitempty"`
}

// 日志文件轮转参数
const (
	defaultMaxLogSize = 10 << 20 // 单文件上限 10MB
	maxLogBackups     = 5        // 保留最近 5 份
)

// Logger 日志记录器
type Logger struct {
	mu      sync.Mutex
	file    *os.File
	path    string
	maxSize int64
	size    int64
	level   Level
	ring    []Entry
	ringIdx int
	ringCap int
	sinks   []func(Entry)
}

// New 创建日志记录器，日志文件写入用户缓存目录
func New() (*Logger, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	logDir := filepath.Join(dir, "sshtunnel", "logs")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return nil, err
	}
	// 收紧已存在目录的权限（升级用户此前的目录可能为 0755）
	_ = os.Chmod(logDir, 0700)
	logFile := filepath.Join(logDir, fmt.Sprintf("sshtunnel-%s.log", time.Now().Format("2006-01-02")))
	return newLoggerAt(logFile, defaultMaxLogSize)
}

// newLoggerAt 在指定路径创建日志记录器（便于测试轮转）
func newLoggerAt(path string, maxSize int64) (*Logger, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, err
	}
	var size int64
	if st, statErr := f.Stat(); statErr == nil {
		size = st.Size()
	}
	return &Logger{
		file:    f,
		path:    path,
		maxSize: maxSize,
		size:    size,
		level:   LevelDebug,
		ringCap: 1000,
		ring:    make([]Entry, 0, 1000),
	}, nil
}

// NewTest 创建测试用日志记录器（无磁盘 I/O），适用于单元测试
func NewTest() *Logger {
	return &Logger{
		file:    nil,
		level:   LevelDebug,
		ringCap: 1000,
		ring:    make([]Entry, 0, 1000),
	}
}

// AddSink 添加实时推送回调（用于 Wails EventsEmit）
func (l *Logger) AddSink(fn func(Entry)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sinks = append(l.sinks, fn)
}

func (l *Logger) log(lvl Level, tunnelID, msg string) {
	if lvl < l.level {
		return
	}
	entry := Entry{
		Time:     time.Now(),
		Level:    lvl.String(),
		Message:  msg,
		TunnelID: tunnelID,
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// 写文件
	line := fmt.Sprintf("%s [%s] %s\n", entry.Time.Format("2006-01-02 15:04:05"), entry.Level, entry.Message)
	if l.file != nil {
		if l.maxSize > 0 && l.size+int64(len(line)) > l.maxSize {
			l.rotateLocked()
		}
		if l.file != nil {
			n, _ := l.file.WriteString(line)
			l.size += int64(n)
		}
	}
	// 写环形缓冲
	if len(l.ring) < l.ringCap {
		l.ring = append(l.ring, entry)
	} else {
		l.ring[l.ringIdx] = entry
		l.ringIdx = (l.ringIdx + 1) % l.ringCap
	}
	// 推送 sink
	for _, s := range l.sinks {
		go s(entry)
	}
}

func (l *Logger) Debug(msg string) { l.log(LevelDebug, "", msg) }
func (l *Logger) Info(msg string)  { l.log(LevelInfo, "", msg) }
func (l *Logger) Warn(msg string)  { l.log(LevelWarn, "", msg) }
func (l *Logger) Error(msg string) { l.log(LevelError, "", msg) }
func (l *Logger) Debugf(format string, args ...any) {
	l.log(LevelDebug, "", fmt.Sprintf(format, args...))
}
func (l *Logger) Infof(format string, args ...any) {
	l.log(LevelInfo, "", fmt.Sprintf(format, args...))
}
func (l *Logger) Warnf(format string, args ...any) {
	l.log(LevelWarn, "", fmt.Sprintf(format, args...))
}
func (l *Logger) Errorf(format string, args ...any) {
	l.log(LevelError, "", fmt.Sprintf(format, args...))
}
func (l *Logger) TunnelDebug(id, msg string) { l.log(LevelDebug, id, msg) }
func (l *Logger) TunnelInfo(id, msg string)  { l.log(LevelInfo, id, msg) }
func (l *Logger) TunnelWarn(id, msg string)  { l.log(LevelWarn, id, msg) }
func (l *Logger) TunnelError(id, msg string) { l.log(LevelError, id, msg) }

// Recent 返回最近 limit 条日志（按时间顺序）
func (l *Logger) Recent(limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.ring)
	if n == 0 {
		return []Entry{}
	}
	if limit > n {
		limit = n
	}
	// 环形缓冲可能未满，直接返回最后 limit 条
	if n < l.ringCap {
		out := make([]Entry, limit)
		copy(out, l.ring[n-limit:])
		return out
	}
	// 缓冲已满，从 ringIdx 开始是最新条目顺序
	out := make([]Entry, limit)
	start := (l.ringIdx - limit + l.ringCap) % l.ringCap
	for i := 0; i < limit; i++ {
		out[i] = l.ring[(start+i)%l.ringCap]
	}
	return out
}

// rotateLocked 轮转日志文件（调用方需持有锁）
func (l *Logger) rotateLocked() {
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	if l.path == "" {
		return
	}
	// 删除最旧备份
	_ = os.Remove(fmt.Sprintf("%s.%d", l.path, maxLogBackups))
	// 依次后移 .4 -> .5 ... .1 -> .2
	for i := maxLogBackups - 1; i >= 1; i-- {
		old := fmt.Sprintf("%s.%d", l.path, i)
		if _, err := os.Stat(old); err == nil {
			_ = os.Rename(old, fmt.Sprintf("%s.%d", l.path, i+1))
		}
	}
	_ = os.Rename(l.path, l.path+".1")
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		l.size = 0
		return
	}
	l.file = f
	// 若重命名失败（原文件仍存在），按实际大小继续跟踪，避免反复触发轮转
	if st, err := f.Stat(); err == nil {
		l.size = st.Size()
	} else {
		l.size = 0
	}
}

// Close 关闭日志文件
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}
