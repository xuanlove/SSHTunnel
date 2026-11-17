package proxy

import (
	"os"
	"testing"
	"time"
)

// TestCityCatalog 从真实城市库构建下拉索引（耗时约 20~30 秒，-short 时跳过）
func TestCityCatalog(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过耗时的城市索引构建测试")
	}
	cityDB := cityDBSample(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := LoadGeoIPDatabaseFile(cityDB); err != nil {
		t.Fatalf("加载城市库失败: %v", err)
	}
	defer unloadGeoDB()

	var res CityCatalogResponse
	deadline := time.Now().Add(120 * time.Second)
	for {
		res = CityCatalog("CN")
		if res.Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("城市索引构建超时（building=%v）", res.Building)
		}
		time.Sleep(200 * time.Millisecond)
	}

	var gd *CityProvince
	for i := range res.Provinces {
		if res.Provinces[i].Code == "GD" || res.Provinces[i].Name == "Guangdong" {
			gd = &res.Provinces[i]
			break
		}
	}
	if gd == nil {
		t.Fatalf("CN 省份列表中未找到广东: %+v", res.Provinces)
	}
	found := false
	for _, c := range gd.Cities {
		if c == "Shenzhen" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("广东城市列表未包含 Shenzhen: %v", gd.Cities)
	}

	// 未知国家应返回空列表但 ready=true
	other := CityCatalog("ZZ")
	if !other.Ready || len(other.Provinces) != 0 {
		t.Errorf("未知国家应返回空列表，实际 ready=%v len=%d", other.Ready, len(other.Provinces))
	}

	// 索引缓存应已写入磁盘
	if _, err := os.Stat(CityIndexCachePath()); err != nil {
		t.Errorf("城市索引缓存文件未生成: %v", err)
	}
}
