package persist

import (
	"strconv"
	"strings"
	"time"

	"sshtunnel/internal/proxy"
)

// AccessFilter 访问历史检索条件
type AccessFilter struct {
	TunnelID string
	From, To time.Time // 零值表示不限制
	Result   string    // success | failed | denied | 空(全部)
	Limit    int       // ≤0 表示不限（调用方应钳制）
	Offset   int
}

// SearchAccess 检索持久化的访问历史（时间倒序，最新在前）
func SearchAccess(dir string, f AccessFilter) ([]proxy.AccessRecord, error) {
	all, err := ReadJSONLDesc[proxy.AccessRecord](dir, "access-", maxScanRecords)
	if err != nil {
		return nil, err
	}
	out := make([]proxy.AccessRecord, 0, len(all))
	for _, r := range all {
		if f.TunnelID != "" && r.TunnelID != f.TunnelID {
			continue
		}
		if !f.From.IsZero() && r.Time.Before(f.From) {
			continue
		}
		if !f.To.IsZero() && r.Time.After(f.To) {
			continue
		}
		if !matchResult(r, f.Result) {
			continue
		}
		out = append(out, r)
	}
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			return []proxy.AccessRecord{}, nil
		}
		out = out[f.Offset:]
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func matchResult(r proxy.AccessRecord, want string) bool {
	switch want {
	case "success":
		return !r.Denied && strings.HasPrefix(r.Result, "成功")
	case "failed":
		return !r.Denied && strings.HasPrefix(r.Result, "失败")
	case "denied":
		return r.Denied
	default:
		return true // 空（全部）或未知值按全部处理
	}
}

// AccessCSV 将访问记录渲染为 CSV（新→旧，首行为表头）
func AccessCSV(recs []proxy.AccessRecord) string {
	var b strings.Builder
	b.WriteString("time,tunnel,protocol,listen_port,source,target,result,duration_ms,denied\n")
	for _, r := range recs {
		fields := []string{
			r.Time.Format("2006-01-02 15:04:05"),
			r.TunnelName,
			r.Protocol,
			strconv.Itoa(r.ListenPort),
			r.Source,
			r.Target,
			r.Result,
			strconv.FormatInt(r.DurationMs, 10),
			strconv.FormatBool(r.Denied),
		}
		for i, f := range fields {
			fields[i] = csvField(f)
		}
		b.WriteString(strings.Join(fields, ","))
		b.WriteByte('\n')
	}
	return b.String()
}

func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}
