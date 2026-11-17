package proxy

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sshtunnel/internal/config"
)

// TestDownloadCityDatabaseInvalid 下载内容不是有效 mmdb 时应报错且不留残留文件
func TestDownloadCityDatabaseInvalid(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("this is not an mmdb"))
	}))
	defer srv.Close()

	if _, _, err := DownloadCityDatabase(srv.URL); err == nil {
		t.Fatalf("非法内容应导致下载失败")
	}
	if _, err := os.Stat(CityDatabasePath()); err == nil {
		t.Errorf("失败后不应留下数据库文件")
	}
}

// TestDownloadCityDatabaseWrongType 下载到 Country 库时应拒绝（必须为 City 类型）
func TestDownloadCityDatabaseWrongType(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if len(builtinGeoDB) == 0 {
		t.Skip("内置数据库为空")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(builtinGeoDB)
	}))
	defer srv.Close()

	if _, _, err := DownloadCityDatabase(srv.URL); err == nil {
		t.Fatalf("Country 库应被拒绝")
	}
	if _, err := os.Stat(CityDatabasePath()); err == nil {
		t.Errorf("校验失败后不应留下数据库文件")
	}
}

// TestDownloadCityDatabaseHTTPError 非 200 响应应报错
func TestDownloadCityDatabaseHTTPError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, _, err := DownloadCityDatabase(srv.URL); err == nil {
		t.Fatalf("HTTP 500 应导致下载失败")
	}
}

// cityDBSample 返回可用的城市库样本路径；不存在时跳过测试
func cityDBSample(t *testing.T) string {
	t.Helper()
	for _, p := range []string{
		os.Getenv("GEOLITE2_CITY_MMDB"),
		"/tmp/Merged-IP.mmdb",
		"/tmp/GeoLite2-City.mmdb",
	} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("未找到城市库样本（可用 GEOLITE2_CITY_MMDB 指定），跳过")
	return ""
}

// TestDownloadCityDatabaseCityServer 提供真实 City 库时应成功并热加载为 City 类型
func TestDownloadCityDatabaseCityServer(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	data, err := os.ReadFile(cityDBSample(t))
	if err != nil {
		t.Skipf("未找到城市库样本，跳过: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	before := CurrentGeoIPStatus()
	defer func() {
		// 恢复测试前状态
		if before.Loaded {
			_ = LoadBuiltinGeoDatabase()
		} else {
			unloadGeoDB()
		}
	}()

	_, size, err := DownloadCityDatabase(srv.URL)
	if err != nil {
		t.Fatalf("下载失败: %v", err)
	}
	if size != int64(len(data)) {
		t.Errorf("下载大小 = %d, want %d", size, len(data))
	}
	st := CurrentGeoIPStatus()
	if st.Type != GeoDBTypeCity || !st.CityReady {
		t.Errorf("下载后应加载 City 库，实际 type=%s city_ready=%v", st.Type, st.CityReady)
	}
	if !st.CityDBExists {
		t.Errorf("下载后 city_db_exists 应为 true")
	}
	if filepath.Dir(st.CityDBCache) == "" {
		t.Errorf("缓存路径异常: %q", st.CityDBCache)
	}
}

// TestMigrateLegacyCityDatabase 旧缓存文件应自动重命名为 Merged-IP.mmdb
func TestMigrateLegacyCityDatabase(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfgDir := filepath.Join(dir, "sshtunnel")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	legacy := filepath.Join(cfgDir, "GeoLite2-City.mmdb")
	if err := os.WriteFile(legacy, []byte("legacy"), 0644); err != nil {
		t.Fatalf("写入旧文件失败: %v", err)
	}

	got := CityDatabasePath()
	want := filepath.Join(cfgDir, "Merged-IP.mmdb")
	if got != want {
		t.Fatalf("缓存路径 = %q, want %q", got, want)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("旧文件应已被重命名")
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "legacy" {
		t.Errorf("新文件内容不符: %q %v", string(data), err)
	}
}

// TestCityLookup 城市级解析返回国家/省/城市
func TestCityLookup(t *testing.T) {
	cityDB := cityDBSample(t)
	if err := LoadGeoIPDatabaseFile(cityDB); err != nil {
		t.Fatalf("加载城市库失败: %v", err)
	}
	defer unloadGeoDB()

	// 223.5.5.5（阿里 DNS）在该库里可解析到浙江杭州
	info, err := lookupGeo(net.ParseIP("223.5.5.5"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if info.Country != "CN" {
		t.Errorf("国家 = %q, want CN", info.Country)
	}
	if info.City == "" || info.Province == "" {
		t.Fatalf("城市级库应解析出省/城市，实际 %+v", info)
	}

	// 城市规则应能命中真实数据
	fw := mustFW(t, &config.FirewallConfig{
		Enabled: true,
		Cities:  []string{"CN/" + info.ProvinceCode + "/" + info.City},
	}, defaultGeoResolver{})
	if fw.Allow(net.ParseIP("223.5.5.5")) {
		t.Errorf("城市规则 CN/%s/%s 应命中 223.5.5.5", info.ProvinceCode, info.City)
	}
}

// TestRankMirrorsBySpeed 测速后应按速度降序排列，失败镜像排最后
func TestRankMirrorsBySpeed(t *testing.T) {
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("f"), mirrorProbeBytes))
	}))
	defer fast.Close()

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("s"), 32*1024)
		for i := 0; i < 32; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer slow.Close()

	dead := "http://127.0.0.1:1/dead.mmdb"
	ranked := rankMirrorsBySpeed([]string{slow.URL, dead, fast.URL})
	if len(ranked) != 3 {
		t.Fatalf("排序结果数量 = %d, want 3", len(ranked))
	}
	if ranked[0] != fast.URL {
		t.Errorf("最快镜像应排第一: got %s, want %s", ranked[0], fast.URL)
	}
	if ranked[1] != slow.URL {
		t.Errorf("较慢镜像应排第二: got %s, want %s", ranked[1], slow.URL)
	}
	if ranked[2] != dead {
		t.Errorf("测速失败的镜像应排最后: got %s, want %s", ranked[2], dead)
	}
}

// TestProbeMirrorRangeHeader 测速应携带 Range 且只读取采样大小
func TestProbeMirrorRangeHeader(t *testing.T) {
	var gotRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRange = r.Header.Get("Range")
		_, _ = w.Write(bytes.Repeat([]byte("x"), mirrorProbeBytes*2))
	}))
	defer srv.Close()

	n, elapsed, err := probeMirror(srv.URL, mirrorProbeBytes)
	if err != nil {
		t.Fatalf("测速失败: %v", err)
	}
	if gotRange != "bytes=0-1048575" {
		t.Errorf("Range 头 = %q, want bytes=0-1048575", gotRange)
	}
	if n != mirrorProbeBytes {
		t.Errorf("采样字节 = %d, want %d", n, mirrorProbeBytes)
	}
	if elapsed <= 0 {
		t.Errorf("耗时应大于 0")
	}
}

// TestDownloadCityDatabaseMultiMirrorFallback 首个镜像不可用时应按测速顺序回退到可用镜像
func TestDownloadCityDatabaseMultiMirrorFallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	data, err := os.ReadFile(cityDBSample(t))
	if err != nil {
		t.Skipf("未找到城市库样本，跳过: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	old := GeoIPDownloadMirrors
	GeoIPDownloadMirrors = []string{"http://127.0.0.1:1/dead.mmdb", srv.URL}
	defer func() { GeoIPDownloadMirrors = old }()

	before := CurrentGeoIPStatus()
	defer func() {
		if before.Loaded {
			_ = LoadBuiltinGeoDatabase()
		} else {
			unloadGeoDB()
		}
	}()

	src, size, err := DownloadCityDatabase("")
	if err != nil {
		t.Fatalf("多镜像下载应回退成功: %v", err)
	}
	if src != srv.URL {
		t.Errorf("实际下载地址 = %q, want %q", src, srv.URL)
	}
	if size != int64(len(data)) {
		t.Errorf("下载大小 = %d, want %d", size, len(data))
	}
	if st := CurrentGeoIPStatus(); !st.CityReady {
		t.Errorf("下载后应加载 City 库")
	}
}
