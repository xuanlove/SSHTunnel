# 内置 GeoIP 数据库

`GeoLite2-Country.mmdb` 为**编译时内嵌**的 GeoIP 国家数据库，用于代理监听器防火墙的「GeoIP 国家」规则。用户无需下载任何文件即可使用。

> 城市级过滤需要更高精度的 **Merged-IP** 数据库（城市 + ASN 合并库，约 93MB），不内嵌到二进制，改为在「系统设置 → GeoIP 城市级数据库」中按需下载并启用（下载后保存于用户配置目录的 `sshtunnel/Merged-IP.mmdb`）。

## 数据来源与许可

- 数据提供方：[MaxMind](https://www.maxmind.com) GeoLite2 Country 数据库
- 许可协议：CC BY-SA 4.0（GeoLite2 EULA）
- 获取方式：公开镜像 `P3TERX/GeoLite.mmdb`（MaxMind 官方库的定期镜像）
- 内置时间：2026-11-17

> 本项目对 GeoLite2 数据的使用遵循 CC BY-SA 4.0 署名要求，仅用于 IP 归属国家判定，不对数据本身做修改后转售。

## 覆盖范围说明

本库为 **Country 级**数据（精确到国家），不含城市/ISP 信息。私有地址、保留地址、部分 Anycast IP（如 1.1.1.1）可能无归属记录，此时国家规则按「未命中」处理：

- 黑名单模式：放行
- 白名单模式：拒绝（fail-closed）

如需城市信息，请下载 Merged-IP 城市库：
`https://github.com/NetworkCats/Merged-IP-Data/releases/latest/download/Merged-IP.mmdb`，
也可直接在 WEB/桌面界面「系统设置」中点击「下载城市数据库」（程序会校验库类型后落盘）。

## 如何覆盖或更新

运行时可指定外部数据库，覆盖内置数据：

```bash
# 使用外部 GeoLite2-Country.mmdb（推荐定期从 MaxMind 官方更新）
./sshtunnel --mode=web --geoip-db /path/to/GeoLite2-Country.mmdb

# 禁用 GeoIP（此时「GeoIP 国家」规则不生效，IP/IP 段规则不受影响）
./sshtunnel --mode=web --geoip-db none
```

要更新**内置**数据，替换本目录下的 `GeoLite2-Country.mmdb` 后重新编译即可：

```bash
# 从公开镜像获取最新版
curl -fsSL -o internal/proxy/geodata/GeoLite2-Country.mmdb \
  https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-Country.mmdb

# 重新编译（数据库会被 go:embed 打包进二进制）
go build -o releases/sshtunnel-linux-amd64 .
```
