package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang"
)

// mmdbCityRecord 城市库（GeoLite2-City / Merged-IP 等）的通用解码结构。
// 直接使用 maxminddb 读取，避免 geoip2 对数据库类型的白名单限制。
type mmdbCityRecord struct {
	Country struct {
		IsoCode string `maxminddb:"iso_code"`
	} `maxminddb:"country"`
	Subdivisions []struct {
		IsoCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	City struct {
		Names map[string]string `maxminddb:"names"`
	} `maxminddb:"city"`
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
		TimeZone  string  `maxminddb:"time_zone"`
	} `maxminddb:"location"`
}

// GeoInfo 结构化地理位置（国家 / 省 / 城市）
type GeoInfo struct {
	Country      string // ISO 3166-1 alpha-2，如 CN
	ProvinceCode string // ISO 3166-2 省级后缀，如 GD
	Province     string // 省级英文名，如 Guangdong
	City         string // 城市英文名，如 Shenzhen
}

// GeoResolver 将 IP 解析为结构化地理位置。抽象为接口便于测试替换。
type GeoResolver interface {
	Lookup(ip net.IP) (*GeoInfo, error)
}

// GeoIP 库类型
const (
	GeoDBTypeCountry = "Country"
	GeoDBTypeCity    = "City"
)

// 全局 GeoIP 数据库状态
var (
	geoipMu     sync.RWMutex
	geoipDB     *maxminddb.Reader
	geoipSource string    // "builtin" 或文件路径
	geoipType   string    // Country / City
	geoipSize   int64     // 数据库字节数
	geoipMTime  time.Time // 文件修改时间（内置库为零值）
)

// GeoIPStatus 当前 GeoIP 能力状态（供 WEB/桌面 展示）
type GeoIPStatus struct {
	Loaded       bool   `json:"loaded"`         // 是否已加载数据库
	Type         string `json:"type"`           // Country | City
	Source       string `json:"source"`         // builtin 或文件路径
	SizeBytes    int64  `json:"size_bytes"`     // 数据库大小
	UpdatedAt    string `json:"updated_at"`     // 文件更新时间（内置库为空）
	CityReady    bool   `json:"city_ready"`     // 当前加载的是否为城市级数据
	CityDBCache  string `json:"city_db_cache"`  // 城市库缓存路径
	CityDBExists bool   `json:"city_db_exists"` // 城市库文件是否已下载
	Enabled      bool   `json:"enabled"`        // 设置中是否启用城市库
}

// GeoIPSettings 城市级数据库的用户设置（持久化到 geoip.json）
type GeoIPSettings struct {
	// Enabled 是否启用外部下载的城市级数据库（Merged-IP；关闭时使用内置 Country 库）
	Enabled bool `json:"enabled"`
}

// GeoIPDataDir 返回 GeoIP 数据目录（与配置文件同级的 sshtunnel 目录）
func GeoIPDataDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "sshtunnel")
}

// GeoIPSettingsPath 设置文件路径
func GeoIPSettingsPath() string {
	dir := GeoIPDataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "geoip.json")
}

// CityDatabasePath 下载的城市库缓存路径。
// 数据库来源为 Merged-IP（含城市/ASN），文件名随之改为 Merged-IP.mmdb。
func CityDatabasePath() string {
	dir := GeoIPDataDir()
	if dir == "" {
		return ""
	}
	migrateLegacyCityDatabase(dir)
	return filepath.Join(dir, "Merged-IP.mmdb")
}

// legacyCityDatabaseName 旧版 GeoLite2-City 缓存文件名（用于自动迁移）
const legacyCityDatabaseName = "GeoLite2-City.mmdb"

// migrateLegacyCityDatabase 将旧的 GeoLite2-City.mmdb 重命名为新缓存文件名，避免用户重新下载。
func migrateLegacyCityDatabase(dir string) {
	newPath := filepath.Join(dir, "Merged-IP.mmdb")
	if _, err := os.Stat(newPath); err == nil {
		return
	}
	oldPath := filepath.Join(dir, legacyCityDatabaseName)
	if _, err := os.Stat(oldPath); err != nil {
		return
	}
	_ = os.Rename(oldPath, newPath)
}

// LoadGeoIPSettings 读取设置；文件不存在或解析失败时返回默认（未启用）
func LoadGeoIPSettings() GeoIPSettings {
	var s GeoIPSettings
	path := GeoIPSettingsPath()
	if path == "" {
		return s
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s)
	return s
}

// SaveGeoIPSettings 持久化设置
func SaveGeoIPSettings(s GeoIPSettings) error {
	path := GeoIPSettingsPath()
	if path == "" {
		return errors.New("无法确定配置目录")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// GeoIPDownloadMirrors 城市级数据库下载地址（Merged-IP：城市 + ASN 合并库，数据更完整）。
// 下载前会并发测速，优先使用最快的可用镜像；失败时按速度顺序依次回退。
var GeoIPDownloadMirrors = []string{
	"https://github.com/NetworkCats/Merged-IP-Data/releases/latest/download/Merged-IP.mmdb",
	"https://ghfast.top/https://github.com/NetworkCats/Merged-IP-Data/releases/latest/download/Merged-IP.mmdb",
	"https://ghproxy.net/https://github.com/NetworkCats/Merged-IP-Data/releases/latest/download/Merged-IP.mmdb",
	"https://gh-proxy.com/https://github.com/NetworkCats/Merged-IP-Data/releases/latest/download/Merged-IP.mmdb",
	"https://github.moeyy.xyz/https://github.com/NetworkCats/Merged-IP-Data/releases/latest/download/Merged-IP.mmdb",
	"https://hub.gitmirror.com/https://github.com/NetworkCats/Merged-IP-Data/releases/latest/download/Merged-IP.mmdb",
}

const (
	// mirrorProbeBytes 测速采样大小（1MB）
	mirrorProbeBytes = 1 << 20
	// mirrorProbeTimeout 单个镜像测速超时
	mirrorProbeTimeout = 8 * time.Second
)

// geoipHTTPClient 下载客户端（超时较长，尊重环境变量代理）
var geoipHTTPClient = &http.Client{Timeout: 15 * time.Minute}

// mirrorSpeed 镜像测速结果
type mirrorSpeed struct {
	URL   string
	Bytes int64
	Speed float64 // 字节/秒；0 表示测速失败
}

// probeMirror 对镜像做一次限速采样：请求前 1MB 并测量耗时。
// 即使中途超时，也会用已读取的字节数估算速度。
func probeMirror(url string, probeBytes int) (int64, time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), mirrorProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", "sshtunnel-geoip")
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", probeBytes-1))

	start := time.Now()
	resp, err := geoipHTTPClient.Do(req)
	if err != nil {
		return 0, time.Since(start), err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return 0, time.Since(start), fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	buf := make([]byte, 64*1024)
	var read int64
	for read < int64(probeBytes) {
		n := len(buf)
		if remain := int64(probeBytes) - read; int64(n) > remain {
			n = int(remain)
		}
		m, err := resp.Body.Read(buf[:n])
		read += int64(m)
		if err != nil {
			break
		}
	}
	elapsed := time.Since(start)
	if read == 0 {
		return 0, elapsed, errors.New("未读取到数据")
	}
	return read, elapsed, nil
}

// rankMirrorsBySpeed 并发测速并返回按速度降序排列的镜像地址。
// 测速失败的镜像排在最后，仍会作为兜底尝试。
func rankMirrorsBySpeed(urls []string) []string {
	if len(urls) <= 1 {
		return append([]string(nil), urls...)
	}

	results := make([]mirrorSpeed, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(i int, u string) {
			defer wg.Done()
			n, elapsed, err := probeMirror(u, mirrorProbeBytes)
			if err != nil {
				log.Printf("GeoIP 镜像测速失败 %s: %v", u, err)
				results[i] = mirrorSpeed{URL: u}
				return
			}
			secs := elapsed.Seconds()
			if secs <= 0 {
				secs = 0.001
			}
			results[i] = mirrorSpeed{URL: u, Bytes: n, Speed: float64(n) / secs}
			log.Printf("GeoIP 镜像测速 %s: %.0f KB/s", u, results[i].Speed/1024)
		}(i, u)
	}
	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool { return results[i].Speed > results[j].Speed })
	ranked := make([]string, len(results))
	for i, r := range results {
		ranked[i] = r.URL
	}
	return ranked
}

// DownloadCityDatabase 下载城市级数据库（Merged-IP）到缓存路径并热加载。
// urlOverride 非空时仅使用该地址；否则先并发测速，优先使用最快的镜像。
// 返回实际使用的下载地址与文件大小。
func DownloadCityDatabase(urlOverride string) (string, int64, error) {
	dest := CityDatabasePath()
	if dest == "" {
		return "", 0, errors.New("无法确定配置目录")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return "", 0, err
	}

	urls := GeoIPDownloadMirrors
	if urlOverride != "" {
		urls = []string{urlOverride}
	} else if len(urls) > 1 {
		// 多镜像：先测速，按速度从快到慢依次尝试
		urls = rankMirrorsBySpeed(urls)
		if len(urls) > 0 {
			log.Printf("GeoIP 城市库将优先使用镜像: %s", urls[0])
		}
	}

	var lastErr error
	for _, u := range urls {
		size, err := downloadTo(u, dest)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", u, err)
			continue
		}
		// 下载成功，尝试加载（失败则清理文件，避免留下坏数据）
		if err := LoadGeoIPDatabaseFile(dest); err != nil {
			_ = os.Remove(dest)
			lastErr = fmt.Errorf("%s: %w", u, err)
			continue
		}
		return u, size, nil
	}
	return "", 0, fmt.Errorf("全部下载地址失败: %w", lastErr)
}

// downloadTo 下载到目标路径（先写临时文件），并校验可被解析为 City 库
func downloadTo(url, dest string) (int64, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "sshtunnel-geoip")
	resp, err := geoipHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dest), "geoip-*.tmp")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	size, err := io.Copy(tmp, resp.Body)
	if err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}

	// 校验数据库有效且包含城市数据
	db, err := maxminddb.Open(tmpPath)
	if err != nil {
		return 0, fmt.Errorf("下载内容不是有效的 mmdb: %w", err)
	}
	dbType := db.Metadata.DatabaseType
	db.Close()
	if !strings.Contains(strings.ToLower(dbType), "city") {
		return 0, fmt.Errorf("下载的数据库不是 City 类型（got %q）", dbType)
	}

	if err := os.Rename(tmpPath, dest); err != nil {
		return 0, err
	}
	return size, nil
}

// LoadGeoIPDatabaseFile 加载指定 mmdb 文件并记录库类型
func LoadGeoIPDatabaseFile(path string) error {
	db, err := maxminddb.Open(path)
	if err != nil {
		return fmt.Errorf("加载 GeoIP 数据库失败: %w", err)
	}
	info, err := os.Stat(path)
	var size int64
	var mtime time.Time
	if err == nil {
		size = info.Size()
		mtime = info.ModTime()
	}
	replaceGeoDB(db, path, db.Metadata.DatabaseType, size, mtime)
	return nil
}

// LoadBuiltinGeoDatabase 加载编译期内嵌的 Country 库（无外部文件依赖）
func LoadBuiltinGeoDatabase() error {
	if len(builtinGeoDB) == 0 {
		return errors.New("内置 GeoIP 数据库为空")
	}
	db, err := maxminddb.FromBytes(builtinGeoDB)
	if err != nil {
		return fmt.Errorf("解析内置 GeoIP 数据库失败: %w", err)
	}
	replaceGeoDB(db, "builtin", db.Metadata.DatabaseType, int64(len(builtinGeoDB)), time.Time{})
	return nil
}

// replaceGeoDB 原子替换全局数据库
func replaceGeoDB(db *maxminddb.Reader, source, dbType string, size int64, mtime time.Time) {
	normalized := normalizeDBType(dbType)
	geoipMu.Lock()
	old := geoipDB
	geoipDB = db
	geoipSource = source
	geoipType = normalized
	geoipSize = size
	geoipMTime = mtime
	geoipMu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	// 城市下拉索引随数据库切换重建（内置 Country 库没有城市数据）
	resetCityCatalog()
	if normalized == GeoDBTypeCity {
		ensureCityCatalogAsync()
	}
}

// normalizeDBType 归一化库类型（Country / City / 其他）
func normalizeDBType(t string) string {
	lt := strings.ToLower(t)
	switch {
	case strings.Contains(lt, "city"):
		return GeoDBTypeCity
	case strings.Contains(lt, "country"):
		return GeoDBTypeCountry
	default:
		return t
	}
}

// GeoIPAvailable 当前是否已加载 GeoIP 数据库
func GeoIPAvailable() bool {
	geoipMu.RLock()
	defer geoipMu.RUnlock()
	return geoipDB != nil
}

// CurrentGeoIPStatus 返回当前 GeoIP 状态
func CurrentGeoIPStatus() GeoIPStatus {
	settings := LoadGeoIPSettings()
	cache := CityDatabasePath()
	_, statErr := os.Stat(cache)

	geoipMu.RLock()
	defer geoipMu.RUnlock()
	st := GeoIPStatus{
		Loaded:       geoipDB != nil,
		Type:         geoipType,
		Source:       geoipSource,
		SizeBytes:    geoipSize,
		CityReady:    geoipType == GeoDBTypeCity,
		CityDBCache:  cache,
		CityDBExists: statErr == nil,
		Enabled:      settings.Enabled,
	}
	if !geoipMTime.IsZero() {
		st.UpdatedAt = geoipMTime.Format(time.RFC3339)
	}
	return st
}

// Lookup 使用全局数据库解析地理位置
func lookupGeo(ip net.IP) (*GeoInfo, error) {
	geoipMu.RLock()
	db := geoipDB
	geoipMu.RUnlock()
	if db == nil {
		return nil, errors.New("未加载 GeoIP 数据库")
	}
	var rec mmdbCityRecord
	if err := db.Lookup(ip, &rec); err != nil {
		return nil, err
	}
	info := &GeoInfo{
		Country: strings.ToUpper(rec.Country.IsoCode),
		City:    rec.City.Names["en"],
	}
	if len(rec.Subdivisions) > 0 {
		sub := rec.Subdivisions[0]
		info.Province = sub.Names["en"]
		// ISO 3166-2 形如 CN-GD，取省级后缀
		if i := strings.LastIndex(sub.IsoCode, "-"); i >= 0 {
			info.ProvinceCode = strings.ToUpper(sub.IsoCode[i+1:])
		} else {
			info.ProvinceCode = strings.ToUpper(sub.IsoCode)
		}
	}
	return info, nil
}

// defaultGeoResolver 使用全局 GeoIP 数据库
type defaultGeoResolver struct{}

func (defaultGeoResolver) Lookup(ip net.IP) (*GeoInfo, error) { return lookupGeo(ip) }
