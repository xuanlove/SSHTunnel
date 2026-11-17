package proxy

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/oschwald/maxminddb-golang"
)

// CityProvince 城市库中的省级条目（用于前端下拉选项）
type CityProvince struct {
	Code   string   `json:"code"`   // ISO 3166-2 省级后缀，如 GD
	Name   string   `json:"name"`   // 省级英文名，如 Guangdong
	Cities []string `json:"cities"` // 城市英文名列表，如 Shenzhen
}

// CityCatalogResponse 城市下拉选项响应
type CityCatalogResponse struct {
	Ready     bool           `json:"ready"`     // 索引是否已就绪
	Building  bool           `json:"building"`  // 是否正在构建索引
	Country   string         `json:"country"`   // 请求的国家代码
	Provinces []CityProvince `json:"provinces"` // 该国家的省/城市列表
}

// cityCatalogCache 城市索引的磁盘缓存（按源数据库大小与修改时间校验）
type cityCatalogCache struct {
	SourceSize  int64                     `json:"source_size"`
	SourceMTime int64                     `json:"source_mtime"`
	Countries   map[string][]CityProvince `json:"countries"`
}

var (
	cityCatalogMu       sync.RWMutex
	cityCatalog         map[string][]CityProvince
	cityCatalogBuilding bool
	cityCatalogGen      int
)

// CityIndexCachePath 城市索引缓存文件路径
func CityIndexCachePath() string {
	dir := GeoIPDataDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "geoip-cities.json")
}

// CityCatalog 返回指定国家的省/城市下拉选项。
// 首次调用时若索引尚未构建，会在后台扫描城市库（结果缓存到磁盘）。
func CityCatalog(country string) CityCatalogResponse {
	country = strings.ToUpper(strings.TrimSpace(country))
	ensureCityCatalogAsync()

	cityCatalogMu.RLock()
	building := cityCatalogBuilding
	idx := cityCatalog
	cityCatalogMu.RUnlock()

	resp := CityCatalogResponse{Building: building, Country: country}
	if idx == nil {
		return resp
	}
	resp.Ready = true
	resp.Provinces = idx[country]
	return resp
}

// resetCityCatalog 清空内存索引（数据库切换时调用），并递增代次避免旧构建结果覆盖
func resetCityCatalog() {
	cityCatalogMu.Lock()
	cityCatalog = nil
	cityCatalogBuilding = false
	cityCatalogGen++
	cityCatalogMu.Unlock()
}

// ensureCityCatalogAsync 确保城市索引在后台构建（仅当加载的是外部 City 库）
func ensureCityCatalogAsync() {
	geoipMu.RLock()
	source, dbType := geoipSource, geoipType
	geoipMu.RUnlock()
	if dbType != GeoDBTypeCity || source == "" || source == "builtin" {
		return
	}

	cityCatalogMu.Lock()
	if cityCatalog != nil || cityCatalogBuilding {
		cityCatalogMu.Unlock()
		return
	}
	cityCatalogBuilding = true
	gen := cityCatalogGen
	cityCatalogMu.Unlock()

	go func() {
		data, err := loadOrBuildCityCatalog(source)

		cityCatalogMu.Lock()
		if gen != cityCatalogGen {
			// 构建期间数据库已切换，丢弃结果
			cityCatalogMu.Unlock()
			return
		}
		if err == nil {
			cityCatalog = data
		}
		cityCatalogBuilding = false
		cityCatalogMu.Unlock()

		if err != nil {
			log.Printf("⚠️  构建 GeoIP 城市列表失败: %v", err)
		}
	}()
}

// loadOrBuildCityCatalog 优先读取磁盘缓存，未命中则扫描数据库并写回缓存
func loadOrBuildCityCatalog(source string) (map[string][]CityProvince, error) {
	info, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	if cached := readCityCatalogCache(info.Size(), info.ModTime().UnixNano()); cached != nil {
		return cached, nil
	}
	data, err := buildCityCatalog(source)
	if err != nil {
		return nil, err
	}
	writeCityCatalogCache(data, info.Size(), info.ModTime().UnixNano())
	return data, nil
}

func readCityCatalogCache(size, mtime int64) map[string][]CityProvince {
	path := CityIndexCachePath()
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c cityCatalogCache
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil
	}
	if c.SourceSize != size || c.SourceMTime != mtime || len(c.Countries) == 0 {
		return nil
	}
	return c.Countries
}

func writeCityCatalogCache(countries map[string][]CityProvince, size, mtime int64) {
	path := CityIndexCachePath()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	raw, err := json.Marshal(cityCatalogCache{
		SourceSize:  size,
		SourceMTime: mtime,
		Countries:   countries,
	})
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "geoip-cities-*.tmp")
	if err != nil {
		return
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err == nil {
		tmp.Close()
		_ = os.Rename(tmpPath, path)
		return
	}
	tmp.Close()
	_ = os.Remove(tmpPath)
}

// buildCityCatalog 扫描 mmdb 中所有网段，汇总「国家 → 省 → 城市」去重列表
func buildCityCatalog(path string) (map[string][]CityProvince, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	type provAcc struct {
		code   string
		cities map[string]struct{}
	}
	countries := make(map[string]map[string]*provAcc)

	nets := db.Networks(maxminddb.SkipAliasedNetworks)
	for nets.Next() {
		var rec mmdbCityRecord
		if _, err := nets.Network(&rec); err != nil {
			continue
		}
		cc := strings.ToUpper(strings.TrimSpace(rec.Country.IsoCode))
		if cc == "" {
			continue
		}
		provName, code := "", ""
		if len(rec.Subdivisions) > 0 {
			sub := rec.Subdivisions[0]
			provName = strings.TrimSpace(sub.Names["en"])
			code = strings.ToUpper(sub.IsoCode)
			if i := strings.LastIndex(code, "-"); i >= 0 {
				code = code[i+1:]
			}
			if provName == "" {
				provName = code
			}
		}
		if provName == "" {
			continue
		}
		city := strings.TrimSpace(rec.City.Names["en"])

		provs := countries[cc]
		if provs == nil {
			provs = make(map[string]*provAcc)
			countries[cc] = provs
		}
		acc := provs[provName]
		if acc == nil {
			acc = &provAcc{code: code, cities: make(map[string]struct{})}
			provs[provName] = acc
		}
		if city != "" {
			acc.cities[city] = struct{}{}
		}
	}
	if err := nets.Err(); err != nil {
		return nil, err
	}

	out := make(map[string][]CityProvince, len(countries))
	for cc, provs := range countries {
		list := make([]CityProvince, 0, len(provs))
		for name, acc := range provs {
			cities := make([]string, 0, len(acc.cities))
			for c := range acc.cities {
				cities = append(cities, c)
			}
			sort.Strings(cities)
			list = append(list, CityProvince{Code: acc.code, Name: name, Cities: cities})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
		out[cc] = list
	}
	return out, nil
}
