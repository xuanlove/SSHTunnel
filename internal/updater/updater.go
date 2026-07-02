// Package updater 提供 GitHub Release 版本检测与二进制资产定位能力。
//
// 安装脚本与运行时 --check-update 均依赖此包：通过 GitHub Releases API
// 获取最新发布信息，比较语义化版本号，并按当前平台匹配可下载的二进制资产。
package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// 默认仓库（与 git remote 一致）
const DefaultRepo = "xuanlove/SSHTunnel"

// latestReleaseURL 拼装最新发布 API 地址。
const latestReleaseURL = "https://api.github.com/repos/%s/releases/latest"

// Release 表示一个 GitHub 发布。
type Release struct {
	TagName string  `json:"tag_name"` // 形如 v1.0.0
	Name    string  `json:"name"`     // 发布标题
	HTMLURL string  `json:"html_url"` // 发布页面地址
	Body    string  `json:"body"`     // 发布说明
	Assets  []Asset `json:"assets"`   // 附属二进制
}

// Asset 表示发布中的单个二进制资产。
type Asset struct {
	Name                string `json:"name"`
	BrowserDownloadURL  string `json:"browser_download_url"`
	Size                int64  `json:"size"`
	ContentType         string `json:"content_type"`
}

// httpClient 可被测试替换。
var httpClient = &http.Client{Timeout: 15 * time.Second}

// LatestRelease 获取指定仓库的最新发布信息。repo 为空时使用 DefaultRepo。
func LatestRelease(repo string) (*Release, error) {
	if repo == "" {
		repo = DefaultRepo
	}
	url := fmt.Sprintf(latestReleaseURL, repo)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	// 附带 User-Agent，GitHub API 对无 UA 的请求可能返回 403
	req.Header.Set("User-Agent", "sshsuidao-updater")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 GitHub API 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub API 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var r Release
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("解析发布信息失败: %w", err)
	}
	return &r, nil
}

// CheckUpdate 比较当前版本与最新发布版本。
// 返回最新版本号、是否有更新、发布信息。
func CheckUpdate(current, repo string) (latest string, updateAvailable bool, rel *Release, err error) {
	rel, err = LatestRelease(repo)
	if err != nil {
		return "", false, nil, err
	}
	latest = rel.TagName
	updateAvailable = isNewer(current, latest)
	return
}

// AssetForPlatform 在发布资产中查找匹配 goos/goarch 的二进制下载地址。
// 未指定时默认取当前运行时平台。
func (r *Release) AssetForPlatform(goos, goarch string) (string, error) {
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	want := AssetName(goos, goarch)
	for _, a := range r.Assets {
		if a.Name == want {
			return a.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("发布 %s 未包含 %s/%s 对应的二进制（期望资产名 %s）", r.TagName, goos, goarch, want)
}

// AssetName 按 goos/goarch 返回标准二进制资产文件名。
// 命名规则：sshsuidao-{os}-{arch}[.exe]（Windows 需 .exe 后缀）。
func AssetName(goos, goarch string) string {
	if goos == "windows" {
		return fmt.Sprintf("sshsuidao-%s-%s.exe", goos, goarch)
	}
	return fmt.Sprintf("sshsuidao-%s-%s", goos, goarch)
}

// CompareVersions 比较两个语义化版本号（支持可选的 v 前缀与 pre-release 后缀）。
// 返回 -1 / 0 / 1 表示 a < b / a == b / a > b。
func CompareVersions(a, b string) int {
	// 去除前缀 v/V
	a = strings.TrimPrefix(strings.TrimPrefix(a, "v"), "V")
	b = strings.TrimPrefix(strings.TrimPrefix(b, "v"), "V")
	// 去除 pre-release 后缀（-alpha 等），仅比较主版本
	if i := strings.IndexByte(a, '-'); i >= 0 {
		a = a[:i]
	}
	if i := strings.IndexByte(b, '-'); i >= 0 {
		b = b[:i]
	}
	av := splitVer(a)
	bv := splitVer(b)
	n := len(av)
	if len(bv) > n {
		n = len(bv)
	}
	for i := 0; i < n; i++ {
		ai, bi := at(av, i), at(bv, i)
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
	}
	return 0
}

// isNewer 判断 latest 是否严格大于 current。
func isNewer(current, latest string) bool {
	return CompareVersions(current, latest) < 0
}

func splitVer(s string) []int {
	parts := strings.Split(s, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		out[i] = atoiOrZero(p)
	}
	return out
}

func at(v []int, i int) int {
	if i >= len(v) {
		return 0
	}
	return v[i]
}

func atoiOrZero(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}
