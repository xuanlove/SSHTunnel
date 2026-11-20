package persist

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 写入器参数
const (
	writeQueueSize = 4096           // 异步写入队列长度（满则丢弃）
	maxScanRecords = 200000         // 读取分片时的记录安全上限
	retentionTick  = 24 * time.Hour // 过期清理周期
)

// Writer 异步 JSONL 追加写入器：按天分片（prefix-YYYYMMDD.jsonl，0600）。
// 入队非阻塞，队列满时丢弃并计数，不阻塞调用方（代理热路径）。
type Writer struct {
	dir       string
	prefix    string
	retention time.Duration
	queue     chan any
	ctx       context.Context
	cancel    context.CancelFunc
	closed    bool
	closedMu  sync.Mutex
	dropped   atomic.Uint64

	// 以下字段仅由 loop 协程访问
	file *os.File
	day  string
}

// NewWriter 创建并启动写入器；retention 为分片保留时长（0 表示不过期）
func NewWriter(dir, prefix string, retention time.Duration) *Writer {
	_ = os.MkdirAll(dir, 0700)
	ctx, cancel := context.WithCancel(context.Background())
	w := &Writer{
		dir:       dir,
		prefix:    prefix,
		retention: retention,
		queue:     make(chan any, writeQueueSize),
		ctx:       ctx,
		cancel:    cancel,
	}
	if retention > 0 {
		w.cleanup()
	}
	go w.loop()
	return w
}

// Enqueue 非阻塞入队一条记录（由写入协程异步落盘）
func (w *Writer) Enqueue(v any) {
	w.closedMu.Lock()
	if w.closed {
		w.closedMu.Unlock()
		return
	}
	w.closedMu.Unlock()
	select {
	case w.queue <- v:
	default:
		w.dropped.Add(1)
	}
}

// Dropped 返回因队列满而丢弃的记录数
func (w *Writer) Dropped() uint64 { return w.dropped.Load() }

// Close 停止写入器（未落盘的队列条目将被丢弃）
func (w *Writer) Close() {
	w.closedMu.Lock()
	if w.closed {
		w.closedMu.Unlock()
		return
	}
	w.closed = true
	w.closedMu.Unlock()
	w.cancel()
}

func (w *Writer) loop() {
	ticker := time.NewTicker(retentionTick)
	defer ticker.Stop()
	for {
		select {
		case <-w.ctx.Done():
			w.closeFile()
			return
		case v := <-w.queue:
			w.write(v)
		case <-ticker.C:
			if w.retention > 0 {
				w.cleanup()
			}
		}
	}
}

func (w *Writer) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	day := time.Now().Format("20060102")
	if w.file == nil || w.day != day {
		w.closeFile()
		// prefix 约定自带尾部分隔（如 "access-"），分片名 prefix+YYYYMMDD.jsonl
		path := filepath.Join(w.dir, fmt.Sprintf("%s%s.jsonl", w.prefix, day))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return
		}
		w.file, w.day = f, day
	}
	_, _ = w.file.Write(append(b, '\n'))
}

func (w *Writer) closeFile() {
	if w.file != nil {
		_ = w.file.Close()
		w.file = nil
		w.day = ""
	}
}

func (w *Writer) cleanup() {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-w.retention).Format("20060102")
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, w.prefix) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, w.prefix), ".jsonl")
		if len(day) == 8 && day < cutoff {
			_ = os.Remove(filepath.Join(w.dir, name))
		}
	}
}

// shardFiles 返回按天降序（新→旧）排列的分片文件路径
func shardFiles(dir, prefix string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".jsonl")
		if len(day) == 8 {
			names = append(names, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}

// ReadJSONLDesc 按分片天降序读取记录（整体新→旧），最多返回 maxRecords 条（0 表示不限）
func ReadJSONLDesc[T any](dir, prefix string, maxRecords int) ([]T, error) {
	var out []T
	for _, path := range shardFiles(dir, prefix) {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		var lines []json.RawMessage
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) > 0 {
				lines = append(lines, json.RawMessage(line))
			}
		}
		_ = f.Close()
		for i := len(lines) - 1; i >= 0; i-- {
			var v T
			if json.Unmarshal(lines[i], &v) == nil {
				out = append(out, v)
				if maxRecords > 0 && len(out) >= maxRecords {
					return out, nil
				}
			}
		}
	}
	return out, nil
}

// WriteFileAtomic 原子写入文件（0600：临时文件 + fsync + rename）
func WriteFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
