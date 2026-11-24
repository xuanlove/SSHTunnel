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
	// onError 可选错误回调（由调用方在创建时注入；nil 时静默忽略错误）。
	// 仅由 loop 协程读取，构造后不再变更，无需额外同步。
	onError func(err error)

	// 以下字段仅由 loop 协程访问
	file *os.File
	day  string
}

// NewWriter 创建并启动写入器；retention 为分片保留时长（0 表示不过期）
func NewWriter(dir, prefix string, retention time.Duration) *Writer {
	return newWriter(dir, prefix, retention, nil)
}

// NewWriterWithErrorLog 同 NewWriter，另注入写错误回调（如接入日志），
// 保持 NewWriter 原签名向后兼容。
func NewWriterWithErrorLog(dir, prefix string, retention time.Duration, onError func(err error)) *Writer {
	return newWriter(dir, prefix, retention, onError)
}

func newWriter(dir, prefix string, retention time.Duration, onError func(err error)) *Writer {
	_ = os.MkdirAll(dir, 0700)
	ctx, cancel := context.WithCancel(context.Background())
	w := &Writer{
		dir:       dir,
		prefix:    prefix,
		retention: retention,
		queue:     make(chan any, writeQueueSize),
		ctx:       ctx,
		cancel:    cancel,
		onError:   onError,
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
			// 分片打开失败（磁盘满/权限/目录异常等）：上报错误并丢弃本条，避免静默丢数据
			w.reportError(fmt.Errorf("打开历史分片 %s 失败: %w", path, err))
			return
		}
		w.file, w.day = f, day
	}
	if _, err := w.file.Write(append(b, '\n')); err != nil {
		w.reportError(fmt.Errorf("写入历史分片 %s%s.jsonl 失败: %w", w.prefix, w.day, err))
	}
}

// reportError 上报写错误（未注入回调时静默）
func (w *Writer) reportError(err error) {
	if w.onError != nil {
		w.onError(err)
	}
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

// ReadJSONLDesc 按分片天降序读取记录（整体新→旧），最多返回 maxRecords 条（0 表示不限）。
// 顺序扫描分片时仅以环形缓冲保留最后 maxScanRecords 行，内存占用不随分片大小增长。
func ReadJSONLDesc[T any](dir, prefix string, maxRecords int) ([]T, error) {
	var out []T
	keep := maxRecords
	if keep <= 0 || keep > maxScanRecords {
		keep = maxScanRecords
	}
	for _, path := range shardFiles(dir, prefix) {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		// 环形缓冲保留最后 keep 行（0 = ring 首元素为最旧）
		ring := make([]json.RawMessage, 0, keep)
		head := 0
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			// 必须复制：scanner 复用底层缓冲区
			raw := make(json.RawMessage, len(line))
			copy(raw, line)
			if len(ring) < keep {
				ring = append(ring, raw)
			} else {
				ring[head] = raw
				head = (head + 1) % keep
			}
		}
		_ = f.Close()
		// 倒序遍历（新→旧）：head 指向最旧一条，最新一条位于 head-1
		for i := len(ring) - 1; i >= 0; i-- {
			var v T
			if json.Unmarshal(ring[(head+i)%len(ring)], &v) == nil {
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
