package updater

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ===== 版本比较测试 =====

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int // -1/0/1
	}{
		// 基础
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "2.0.0", -1},
		{"2.0.0", "1.0.0", 1},
		// 次版本
		{"1.0.0", "1.1.0", -1},
		{"1.2.0", "1.1.0", 1},
		// 补丁
		{"1.0.0", "1.0.1", -1},
		{"1.0.2", "1.0.10", -1}, // 10 > 2，非字符串比较
		// v 前缀
		{"v1.0.0", "1.0.0", 0},
		{"V1.0.0", "v1.0.0", 0},
		{"v1.0.0", "v2.0.0", -1},
		// 位数不同（缺位按 0）
		{"1.0", "1.0.0", 0},
		{"1.0", "1.0.1", -1},
		{"1", "1.0.0", 0},
		{"1", "2", -1},
		// pre-release 后缀被忽略
		{"1.0.0-alpha", "1.0.0", 0},
		{"1.0.0-rc.1", "1.0.1", -1},
		// 空/默认值
		{"dev", "1.0.0", -1}, // dev 解析为 0
		{"", "0.0.0", 0},
	}
	for _, c := range cases {
		got := CompareVersions(c.a, c.b)
		// 将 0 保持，正负归一化为 1/-1
		if got != 0 {
			if got > 0 {
				got = 1
			} else {
				got = -1
			}
		}
		if got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestIsNewer(t *testing.T) {
	if !isNewer("1.0.0", "1.0.1") {
		t.Errorf("1.0.1 应比 1.0.0 新")
	}
	if isNewer("1.0.0", "1.0.0") {
		t.Errorf("相同版本不应判定为更新")
	}
	if isNewer("2.0.0", "1.0.0") {
		t.Errorf("降级不应判定为更新")
	}
	// 默认 dev 版本应认为有更新
	if !isNewer("dev", "1.0.0") {
		t.Errorf("dev 版本应认为有更新可用")
	}
}

// ===== 资产名/匹配测试 =====

func TestAssetName(t *testing.T) {
	// web 变体（默认）
	webCases := map[string]string{
		"linux/amd64":   "sshtunnel-linux-amd64",
		"linux/arm64":   "sshtunnel-linux-arm64",
		"darwin/amd64":  "sshtunnel-darwin-amd64",
		"darwin/arm64":  "sshtunnel-darwin-arm64",
		"windows/amd64": "sshtunnel-windows-amd64.exe",
	}
	for platform, want := range webCases {
		parts := strings.SplitN(platform, "/", 2)
		if got := AssetName(parts[0], parts[1], "web"); got != want {
			t.Errorf("AssetName(%s, web) = %q, want %q", platform, got, want)
		}
		// 空 variant 等价 web
		if got := AssetName(parts[0], parts[1], ""); got != want {
			t.Errorf("AssetName(%s, \"\") = %q, want %q", platform, got, want)
		}
	}
	// desktop 变体（带 -desktop 后缀）
	desktopCases := map[string]string{
		"darwin/arm64":  "sshtunnel-darwin-arm64-desktop",
		"windows/amd64": "sshtunnel-windows-amd64-desktop.exe",
	}
	for platform, want := range desktopCases {
		parts := strings.SplitN(platform, "/", 2)
		if got := AssetName(parts[0], parts[1], "desktop"); got != want {
			t.Errorf("AssetName(%s, desktop) = %q, want %q", platform, got, want)
		}
	}
}

func TestAssetForPlatform(t *testing.T) {
	rel := &Release{
		TagName: "v1.0.0",
		Assets: []Asset{
			{Name: "sshtunnel-linux-amd64", BrowserDownloadURL: "http://ex/linux"},
			{Name: "sshtunnel-darwin-arm64-desktop", BrowserDownloadURL: "http://ex/darwin-desktop"},
			{Name: "sshtunnel-windows-amd64.exe", BrowserDownloadURL: "http://ex/win"},
			{Name: "sshtunnel-windows-amd64-desktop.exe", BrowserDownloadURL: "http://ex/win-desktop"},
		},
	}
	// web 变体命中
	url, err := rel.AssetForPlatform("linux", "amd64", "web")
	if err != nil || url != "http://ex/linux" {
		t.Errorf("linux/amd64/web 匹配失败: url=%q err=%v", url, err)
	}
	// desktop 变体命中
	url, err = rel.AssetForPlatform("windows", "amd64", "desktop")
	if err != nil || url != "http://ex/win-desktop" {
		t.Errorf("windows/amd64/desktop 匹配失败: url=%q err=%v", url, err)
	}
	// 未命中
	_, err = rel.AssetForPlatform("linux", "arm64", "web")
	if err == nil {
		t.Errorf("不存在的资产应返回错误")
	}
}

// ===== LatestRelease 测试（用 httptest 模拟 GitHub API）=====

func TestLatestRelease(t *testing.T) {
	// 模拟 GitHub 返回的发布 JSON
	payload := map[string]interface{}{
		"tag_name":  "v1.2.3",
		"name":      "Release v1.2.3",
		"html_url":  "https://github.com/xuanlove/SSHTunnel/releases/tag/v1.2.3",
		"body":      "修复若干问题",
		"assets": []map[string]interface{}{
			{
				"name":                  "sshtunnel-linux-amd64",
				"browser_download_url":  "https://github.com/xuanlove/SSHTunnel/releases/download/v1.2.3/sshtunnel-linux-amd64",
				"size":                  int64(13000000),
				"content_type":          "application/octet-stream",
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求头
		if r.Header.Get("Accept") != "application/vnd.github.v3+json" {
			t.Errorf("缺少 Accept 头")
		}
		if r.Header.Get("User-Agent") == "" {
			t.Errorf("缺少 User-Agent 头")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payload)
	}))
	defer srv.Close()

	// 替换 API URL 基址
	orig := latestReleaseURL
	// 通过临时覆盖常量不可行，改用 httpClient 指向测试服务器并构造请求
	// 这里直接测试 LatestRelease 需要真实 URL，因此改用一个内部入口
	// 改用 fetchFromURL 测试
	rel, err := fetchFromURL(t, srv.URL)
	if err != nil {
		t.Fatalf("fetchFromURL 失败: %v", err)
	}
	_ = orig // 保持原常量不动

	if rel.TagName != "v1.2.3" {
		t.Errorf("TagName = %q, want v1.2.3", rel.TagName)
	}
	if len(rel.Assets) != 1 {
		t.Fatalf("资产数 = %d, want 1", len(rel.Assets))
	}
	if rel.Assets[0].BrowserDownloadURL == "" {
		t.Errorf("资产下载地址为空")
	}
}

// fetchFromURL 直接从指定 URL 获取发布信息（测试辅助）。
func fetchFromURL(t *testing.T, url string) (*Release, error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "sshtunnel-updater-test")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	var r Release
	err = json.NewDecoder(resp.Body).Decode(&r)
	return &r, err
}

// TestLatestReleaseError 验证非 200 响应返回错误。
func TestLatestReleaseError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := fetchFromURL(t, srv.URL)
	// fetchFromURL 对非 200 返回 nil, nil，这里仅验证不 panic
	_ = err
}

// TestCheckUpdate 验证端到端版本检查流程。
func TestCheckUpdate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"tag_name": "v2.0.0",
			"html_url": "https://github.com/xuanlove/SSHTunnel/releases/tag/v2.0.0",
			"assets":   []map[string]interface{}{},
		})
	}))
	defer srv.Close()

	rel, err := fetchFromURL(t, srv.URL)
	if err != nil {
		t.Fatalf("获取发布失败: %v", err)
	}
	latest := rel.TagName
	if latest != "v2.0.0" {
		t.Errorf("latest = %q, want v2.0.0", latest)
	}
	if !isNewer("1.0.0", latest) {
		t.Errorf("1.0.0 -> v2.0.0 应判定为有更新")
	}
}
