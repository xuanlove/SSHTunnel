# SSHTunnel

> 一款跨平台的 SSH 隧道管理工具，基于 Wails v2 + Go 1.27.4 + Vue 3 构建。
> 支持多跳跳板链、多协议代理（HTTP/HTTPS/SOCKS4/SOCKS5）、本地端口转发、自动重连、实时日志推送，并提供**桌面端**与 **WEB 面板**双形态管理界面。

[![Go Version](https://img.shields.io/badge/Go-1.27.4+-00ADD8?logo=go)](https://go.dev)
[![Wails](https://img.shields.io/badge/Wails-v2.12-00E5FF)](https://wails.io)
[![Vue](https://img.shields.io/badge/Vue-3.5-42b883?logo=vue.js)](https://vuejs.org)
[![Release](https://img.shields.io/github/v/release/xuanlove/SSHTunnel?include_prereleases)](https://github.com/xuanlove/SSHTunnel/releases)

---

## 目录

- [项目介绍](#项目介绍)
- [功能特性](#功能特性)
- [快速开始](#快速开始)
- [安装](#安装)
- [使用方式](#使用方式)
- [配置说明](#配置说明)
- [WEB API 文档](#web-api-文档)
- [项目结构](#项目结构)
- [代码说明](#代码说明)
- [开发规范](#开发规范)
- [构建与发布](#构建与发布)
- [安全建议](#安全建议)
- [版本更新说明](#版本更新说明)
- [技术栈](#技术栈)
- [许可证](#许可证)

---

## 项目介绍

SSHTunnel 是一个用于管理 SSH 隧道的跨平台工具。它把 SSH 跳板、本地端口转发与多协议代理服务统一在一个界面中管理，既可作为桌面应用本地运行，也可作为 WEB 服务部署在远程服务器上。

**典型使用场景：**

- 通过多跳跳板机访问内网服务，并将内网端口转发到本地
- 在跳板机上快速开启 HTTP / SOCKS5 代理，供团队临时使用
- 服务器部署：以 systemd 服务常驻运行 WEB 面板，浏览器远程管理所有隧道
- 桌面日常使用：本地启动 GUI，可视化编排跳板链与代理监听器

**双形态设计：** 同一份代码编译出两种二进制：

| 变体 | 资产命名 | 说明 |
|------|----------|------|
| `web`（默认） | `sshtunnel-{os}-{arch}[.exe]` | 纯 WEB 模式，CGO 禁用，可纯 Go 交叉编译，适合服务器 |
| `desktop` | `sshtunnel-{os}-{arch}-desktop[.exe]` | 含 Wails WebView，CGO 启用，原生 GUI 窗口 |

`--check-update` 与安装脚本会按当前变体匹配对应资产，互不干扰。

---

## 功能特性

### 隧道能力

- **多跳跳板链**：支持任意层级的 SSH 跳板，逐跳串联建立 SSH 连接
- **本地端口转发**：经典的 SSH `-L` 本地转发，支持多个转发规则
- **多协议代理**：单条隧道可同时监听 HTTP / HTTPS / SOCKS4 / SOCKS5 多端口
- **认证方式**：密码认证 / 密钥认证（密钥以 PEM 文本形式存储，便于跨设备同步）；每跳须配置可信的主机密钥指纹（`SHA256:...`，防中间人）；服务器仅保留键盘交互认证时自动回退
- **自动重连**：SSH 断开后指数退避重连（初始 2s，上限 60s，可配置最大次数与最大退避间隔）
- **SSH 心跳**：可配置 KeepAlive 间隔（默认关闭，建议 60 秒），及时发现僵死连接并触发重连
- **开机启动**：每条隧道可独立勾选「开机启动」，程序/服务启动时自动拉起（配合 systemd 服务即开机自启）
- **运行统计**：仪表盘卡片展示累计上传/累计下载与活跃、累计连接数，监听器标签悬浮可查看单项明细
- **节点状态页**：汇总本节点运行时长、隧道与监听服务数、累计流量；以节点卡片展示每条隧道的状态、活动/累计连接与累计上下行，点击卡片可查看该节点的当前连接与该节点访问历史（按隧道独立查询，最多 500 条）
- **流量趋势与告警**：每 10 秒采样全局与各隧道的连接数/流量并保留 24 小时（快照持久化，重启后趋势连续），仪表盘与节点详情可查看 1h/6h/24h 趋势；支持隧道断开、连接数阈值、流量阈值三类规则，命中持续窗口后通过 Webhook（通用 JSON/企业微信/飞书）通知，设置页保留最近告警（持久化，重启不丢）
- **访问历史持久化与审计**：访问历史落盘为按天 JSONL 分片（保留 30 天，异步写入不阻塞代理热路径），重启自动恢复；节点详情支持按时间范围（1h/6h/24h/7d/全部）与结果（成功/失败/拒绝）筛选，并可一键导出 CSV
- **代理访问日志**：监听器可开启访问日志，记录每次访问的目标地址、结果与耗时；同一条记录也会进入「节点状态」的访问历史（进程内保留最近 2000 条），支持「操作日志」页按隧道过滤与导出
- **端口冲突检测**：监听启动前自动检测端口占用，避免冲突
- **代理防火墙**：每个代理监听端口可独立配置简易防火墙，支持黑名单（默认）/白名单模式、自定义 IP 与 IP 段（IPv4 + IPv6 CIDR）、GeoIP 国家规则（**内置 GeoLite2-Country，开箱即用**）与城市级规则（按需下载 Merged-IP 城市库）。配置了城市规则但未加载城市级数据库时，监听器会启动失败并给出明确提示，避免规则静默失效
- **代理连通性自检**：每个代理监听器可一键「测试代理端口」，经该监听端口访问 `https://api.ip.sb/ip` 并显示出口 IP，直观验证隧道是否走通

### 代理协议细节

| 协议 | 认证 | 说明 |
|------|------|------|
| HTTP | Basic Auth（可选） | 支持 `CONNECT` 方法代理 HTTPS 流量 |
| HTTPS | Basic Auth（可选） | 需提供 TLS 证书（可一键生成自签证书） |
| SOCKS4 | UserID（可选） | 兼容 SOCKS4a（域名直连） |
| SOCKS5 | 用户名/密码（可选） | 实现 RFC 1928 / RFC 1929 |

### 管理界面

- **桌面端**：Wails + WebView 原生应用（Windows / Linux / macOS）
- **WEB 面板**：内置 HTTP/HTTPS 服务器，浏览器即可管理
- **访问密码**：可在「系统设置」中随时开启/关闭（**默认不开启**），凭据以 bcrypt 哈希保存于服务器本地
- **实时推送**：日志与隧道状态变更通过 WebSocket 实时推送到前端
- **JWT 鉴权**：开启访问密码后，登录获取 Token（24 小时有效）
- **TLS 支持**：WEB 面板与 HTTPS 代理均可使用 TLS
- **面板 HTTPS 可在线配置**：「系统设置」中开启 HTTPS 并直接粘贴证书/私钥内容（PEM），保存后监听热重启立即生效
- **证书自动部署**：提供 Token 鉴权的 certd 部署 API，可自动替换 WEB 面板或 HTTPS 隧道监听器的证书并重启隧道
- **最小权限账户引导**：新建/编辑隧道的跳板链下方内置 SSH 账户创建命令示例，支持公钥或密码认证，可按当前用户名一键生成并复制（无登录 Shell、仅开放 TCP 转发）

---

## 快速开始

### 一键安装（Linux）

```bash
curl -fsSL https://raw.githubusercontent.com/xuanlove/SSHTunnel/main/scripts/install.sh | sudo bash
```

默认会安装为 **systemd 服务并以 WEB 模式运行**（默认监听 `127.0.0.1:8080`，开机自启；未提供 `WEB_AUTH` 时自动生成随机访问密码并写入 `/etc/sshtunnel/sshtunnel.env`）。安装完成后：

```bash
sshtunnel --version                # 查看版本
systemctl status sshtunnel         # 查看服务状态
journalctl -u sshtunnel -f         # 实时日志
```

浏览器访问 `http://127.0.0.1:8080` 进入管理面板（远程访问时设置 `WEB_HOST=0.0.0.0`，并确保已配置访问密码与防火墙限制）。

### 下载预编译二进制

前往 [Releases 页面](https://github.com/xuanlove/SSHTunnel/releases/latest) 下载对应平台的二进制：

| 文件 | 平台 | 变体 |
|------|------|------|
| `sshtunnel-linux-amd64` | Linux x86_64 | web |
| `sshtunnel-linux-arm64` | Linux arm64 | web |
| `sshtunnel-darwin-amd64` | macOS Intel | web |
| `sshtunnel-darwin-arm64` | macOS Apple Silicon | web |
| `sshtunnel-darwin-amd64-desktop` | macOS Intel | desktop |
| `sshtunnel-darwin-arm64-desktop` | macOS Apple Silicon | desktop |
| `sshtunnel-windows-amd64.exe` | Windows x86_64 | web（控制台） |
| `sshtunnel-windows-amd64-desktop.exe` | Windows x86_64 | desktop（GUI） |

> macOS 桌面版由 GitHub Actions 的 macOS runner 原生构建（见 [.github/workflows/build-macos-desktop.yml](.github/workflows/build-macos-desktop.yml)），也可在 macOS 本地构建。

---

## 安装

### 方式一：安装脚本（推荐）

默认安装为 **systemd 服务（WEB 模式，开机自启）**，可用 `--no-service` 仅安装二进制：

```bash
# 安装最新版（默认注册 systemd 服务并启动）
curl -fsSL https://raw.githubusercontent.com/xuanlove/SSHTunnel/main/scripts/install.sh | sudo bash

# 非交互配置服务参数（管道安装场景）
curl -fsSL https://raw.githubusercontent.com/xuanlove/SSHTunnel/main/scripts/install.sh | \
  sudo WEB_HOST=0.0.0.0 WEB_PORT=8090 WEB_AUTH=admin:yourpassword bash

# 安装指定版本
sudo bash scripts/install.sh -v v1.1.0

# 仅安装二进制，不注册 systemd 服务
sudo bash scripts/install.sh --no-service

# 升级模式（保留现有配置，仅替换二进制；服务自动重启生效）
sudo bash scripts/install.sh -u
```

脚本选项：

| 选项 | 说明 |
|------|------|
| `-r, --repo owner/repo` | GitHub 仓库（默认 `xuanlove/SSHTunnel`） |
| `-v, --version VERSION` | 安装指定版本而非最新（如 `v1.1.0`，缺 `v` 前缀自动补全） |
| `-d, --dir PATH` | 安装目录（默认 `/usr/local/bin`） |
| `-s, --service` | 安装为 systemd 服务（默认启用，保留兼容） |
| `-n, --no-service` | 仅安装二进制，不安装 systemd 服务 |
| `-u, --upgrade` | 升级模式：保留配置，仅替换二进制；已安装的 systemd 服务会自动重启（未运行也会拉起），并输出重启结果 |
| `-h, --help` | 显示帮助 |

服务参数环境变量（交互终端下会逐项询问；管道/非交互安装时读取这些变量）：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `WEB_HOST` | `127.0.0.1` | WEB 监听地址（远程访问需显式设为 `0.0.0.0`，并配置访问密码与防火墙） |
| `WEB_PORT` | `8080` | WEB 监听端口 |
| `WEB_AUTH` | 空（自动生成随机密码） | 访问密码 `user:password`；留空时生成 24 位随机密码并写入 `/etc/sshtunnel/sshtunnel.env`（0600） |

下载镜像（脚本会并行测速，从最快的源下载，失败自动切换下一个源）：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `SSHTUNNEL_MIRRORS` | 空 | 追加 GitHub 代理根（空格分隔），如 `"https://ghfast.top https://ghproxy.net"` |
| `SSHTUNNEL_DIRECT` | `1` | 设为 `0` 时不使用 GitHub 直连，只走镜像（内网/自建镜像场景） |
| `SSHTUNNEL_MIRROR_TEST` | `1` | 设为 `0` 时跳过测速，按配置顺序依次尝试 |

```bash
# 指定镜像并跳过测速（按列表顺序尝试）
curl -fsSL https://raw.githubusercontent.com/xuanlove/SSHTunnel/main/scripts/install.sh | \
  sudo SSHTUNNEL_MIRRORS="https://ghfast.top" SSHTUNNEL_MIRROR_TEST=0 bash
```

内置镜像：GitHub 直连、`ghfast.top`、`ghproxy.net`、`gh-proxy.com`、`github.moeyy.xyz`。测速方式为对每个源做 512KB Range 采样，按实测吞吐排序（不支持 Range 的源以首字节延迟兜底）。

### 方式二：systemd 服务部署（根目录脚本）

根目录的 [`install.sh`](install.sh) 面向生产环境部署，从本地预编译二进制安装为加固的 systemd 服务：

```bash
# 交互式菜单
sudo ./install.sh

# 直接安装并启动
sudo ./install.sh install

# 卸载
sudo ./install.sh uninstall

# 查看状态 / 重启 / 日志
sudo ./install.sh status | restart | logs
```

该脚本特性：

- 创建专用系统用户 `SSHTunnel`（`nologin`，无家目录）
- 创建独立目录：`/etc/SSHTunnel`（配置）、`/var/lib/SSHTunnel`（数据）、`/var/log/SSHTunnel`（日志）
- systemd unit 含安全加固：`ProtectSystem=strict`、`PrivateTmp=true`、`NoNewPrivileges=true`、`LimitNOFILE=65536`
- 支持非交互模式：`sudo -E LISTEN_PORT=8090 AUTH_USER=admin AUTH_PASS=xxx ./install.sh install`
- 端口占用检测（`ss` → `netstat` → `/proc/net/tcp` 三级回退）

### 方式三：从源码构建

**依赖：**

- Go 1.27.4+
- Node.js 18+ 与 npm（构建前端）
- 桌面模式额外依赖：
  - Windows：无
  - Linux：`libwebkit2gtk-4.1-dev` `libgtk-3-dev`
  - macOS：Xcode Command Line Tools

```bash
# 克隆项目
git clone https://github.com/xuanlove/SSHTunnel.git
cd SSHTunnel

# 构建前端
cd frontend && npm install && npm run build && cd ..

# 构建当前平台桌面端（CGO 启用）
go build -o build/bin/sshtunnel .

# 仅构建 WEB 模式二进制（CGO 禁用，可交叉编译）
CGO_ENABLED=0 go build -o build/bin/sshtunnel-web .
```

---

## 使用方式

### 运行模式

通过 `--mode` 参数切换：

| 模式 | 说明 |
|------|------|
| `desktop` | 默认。仅启动桌面端应用（原生 GUI 窗口） |
| `web` | 仅启动 WEB 面板（无 GUI 依赖，适合服务器部署） |
| `both` | 同时启动桌面端与 WEB 面板 |

### 桌面模式

```bash
./sshtunnel
# 或显式指定
./sshtunnel --mode=desktop
```

启动后弹出原生窗口，左侧导航包含：仪表盘、配置编辑、日志、设置。

### WEB 模式（无密码）

```bash
./sshtunnel --mode=web --web-port=8090
```

浏览器访问 `http://127.0.0.1:8090` 进入管理面板。非回环地址需要有效的访问密码。

### WEB 模式（密码保护）

两种方式启用访问密码：

1. 启动参数（临时覆盖，重启后以 `settings.json` 为准）：

```bash
./sshtunnel --mode=web --web-port=8090 --auth=admin:yourpassword
```

2. **系统设置页（推荐）**：打开 WEB 面板 → 左侧「系统设置」→「WEB 访问密码」卡片，打开「启用访问密码」开关，填写用户名与密码（至少 6 位）后保存。设置持久化到 `<配置目录>/sshtunnel/settings.json`，重启后仍然生效；无需重启即可下发登录要求。

首次访问将跳转至登录页，输入凭据后获取 JWT Token（24 小时有效）。更换凭据会撤销旧会话并关闭其 WebSocket 连接。

### 启用 HTTPS（WEB 面板）

两种方式：

1. **系统设置页（推荐）**：「系统设置 → WEB 面板 HTTPS（SSL）」，打开「启用 HTTPS」，直接粘贴证书与私钥 PEM 内容后「保存并应用」。保存后监听会在同一端口热重启，**改用 `https://` 重新访问即可**，无需重启进程；设置持久化到 `<配置目录>/sshtunnel/web_tls.json`，证书写入 `certs/pair-*/web.crt|web.key`。
2. 启动参数（会写入同样的持久化配置）：

```bash
./sshtunnel --mode=web --web-port=8443 \
  --tls-cert=/path/to/cert.pem \
  --tls-key=/path/to/key.pem \
  --auth=admin:password
```

> 启用了 HTTPS 的面板在证书无法加载时拒绝启动。热更新失败时保留当前监听。

### 混合模式

```bash
./sshtunnel --mode=both --web-port=8090 --auth=admin:password
```

同时启动桌面端窗口与 WEB 面板，两端的日志与状态变更实时同步。

### 命令行参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--mode` | `desktop` | 运行模式：`desktop` / `web` / `both` |
| `--web-host` | `127.0.0.1` | WEB 监听地址 |
| `--web-port` | `8080` | WEB 监听端口 |
| `--auth` | 空 | 启用密码访问，格式 `user:password`，非回环监听地址必须启用认证 |
| `--tls-cert` | 空 | TLS 证书路径（启用 HTTPS 并持久化到面板设置） |
| `--tls-key` | 空 | TLS 私钥路径（启用 HTTPS 并持久化到面板设置） |
| `--version` | - | 打印版本信息并退出 |
| `--check-update` | - | 检查 GitHub Release 是否有新版本并退出 |
| `--repo` | `xuanlove/SSHTunnel` | GitHub 仓库（owner/repo），用于版本检查 |
| `--geoip-db` | 空（按系统设置） | GeoIP `.mmdb` 路径（Country 或 City）；留空时按系统设置加载（未启用则用内置 Country 库），`none` 禁用（影响代理防火墙国家/城市规则） |

### 版本检查

```bash
# 打印当前版本
./sshtunnel --version

# 检查是否有新版本
./sshtunnel --check-update
```

`--check-update` 退出码：

| 退出码 | 含义 |
|--------|------|
| `0` | 已是最新版本 |
| `1` | 发现新版本 |
| `2` | 检查失败（网络/API 错误） |

### 跳板链简写格式

配置编辑器支持简写格式，使用 `,` 或 `->` 分隔多跳：

```
user1@host1:22 -> user2@host2:2222 -> user3@host3
```

等价于：

```
user1@host1:22, user2@host2:2222, user3@host3
```

每跳格式：`[user@]host[:port]`，省略 `port` 时默认 22。

---

## 配置说明

### 配置文件位置

配置文件存储于用户配置目录：

- Windows：`%APPDATA%\sshtunnel\configs.json`
- Linux：`~/.config/sshtunnel/configs.json`
- macOS：`~/Library/Application Support/sshtunnel/configs.json`

### 配置文件结构

配置文件是一个 JSON 数组，每个元素为一个隧道配置。完整字段说明：

```jsonc
[
  {
    "id": "uuid-自动生成",           // Save 时自动生成回填
    "name": "我的隧道",              // 隧道名称
    "tunnel_type": "proxy",          // "local_forward" 或 "proxy"
    "hop_chain": [                   // SSH 跳板链
      {
        "user": "root",
        "host": "bastion.example.com",
        "port": 22,                  // 省略时默认 22
        "auth_type": "password",     // "password" 或 "key"
        "password": "secret",        // auth_type=password 时填写
        "key_content": "-----BEGIN...", // auth_type=key 时填写（PEM 文本）
        "passphrase": "key-passphrase"  // 密钥口令（可选）
      }
    ],
    "local_forwards": [              // 本地端口转发（tunnel_type=local_forward 时）
      {
        "local_port": 8080,
        "remote_host": "127.0.0.1",
        "remote_port": 80,
        "allow_external": false      // true=监听 0.0.0.0，false=127.0.0.1
      }
    ],
    "proxy_listeners": [             // 代理监听器（tunnel_type=proxy 时）
      {
        "protocol": "socks5",        // http | https | socks4 | socks5
        "listen_port": 1080,
        "allow_external": true,
        "auth": {                    // 可选
          "username": "proxyuser",
          "password": "proxypass"
        },
        "tls": {                      // 仅 https 协议，证书以 PEM 文本保存
          "cert_pem": "-----BEGIN CERTIFICATE-----\n...",
          "key_pem": "-----BEGIN PRIVATE KEY-----\n..."
        },
        "access_log": true            // 是否记录代理访问日志（目标地址、耗时）
      }
    ],
    "auto_reconnect": true,          // 是否自动重连
    "auto_start": true,              // 程序/服务启动时自动启动（配合 systemd 即开机自启）
    "keepalive_seconds": 60,         // SSH 心跳间隔（秒），0 表示关闭
    "max_reconnect_attempts": 0,     // 最大重连次数，0 表示无限
    "reconnect_max_delay_seconds": 60, // 重连最大退避间隔（秒），0 表示默认 60
    "status": "stopped"              // 运行时状态，加载时强制重置为 stopped，不持久化
  }
]
```

### 字段说明

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | string | UUID，新建时自动生成 |
| `name` | string | 隧道名称 |
| `tunnel_type` | string | `local_forward`（本地端口转发）或 `proxy`（代理服务） |
| `hop_chain` | array | SSH 跳板链，逐跳串联 |
| `local_forwards` | array | 本地转发规则（仅 `local_forward` 类型） |
| `proxy_listeners` | array | 代理监听器（仅 `proxy` 类型） |
| `auto_reconnect` | bool | 是否启用自动重连 |
| `auto_start` | bool | 程序/服务启动时自动启动该隧道（配合 systemd 服务实现开机自启） |
| `keepalive_seconds` | int | SSH 心跳间隔（秒），`0` 表示关闭；建议 `60`，可及时发现僵死连接 |
| `max_reconnect_attempts` | int | 最大重连次数，`0` 表示无限重试 |
| `reconnect_max_delay_seconds` | int | 重连指数退避的最大间隔（秒），`0` 表示默认 60 |
| `status` | string | 运行时状态，不持久化（`stopped` / `starting` / `running` / `error`） |

### 代理监听器

单条隧道可配置多个监听器，每个监听器独立指定：

- 协议（`http` / `https` / `socks4` / `socks5`）
- 监听端口
- 是否允许外部访问（`0.0.0.0` 或 `127.0.0.1`）
- 认证配置（HTTP Basic / SOCKS5 用户名密码 / SOCKS4 UserID）
- TLS 证书（仅 HTTPS，采用 PEM 文本方式，可在监听器编辑页直接粘贴或一键生成自签证书）
- 访问日志（`access_log`，记录每次代理访问的目标地址与耗时，默认关闭）
- **简易防火墙**（可选，默认不启用）：黑名单/白名单模式、自定义 IP 与 IP 段、GeoIP 国家规则与城市级规则

防火墙配置示例（`proxy_listeners[].firewall`）：

```jsonc
{
  "enabled": true,
  "mode": "blacklist",              // blacklist（默认，命中拒绝）| whitelist（仅命中放行）
  "ips": [
    "203.0.113.7",                  // 单个 IPv4
    "10.0.0.0/8",                   // IPv4 CIDR
    "2001:db8::/32",                // IPv6 CIDR
    "2001:db8::dead:beef"           // 单个 IPv6
  ],
  "countries": ["CN", "US"],        // GeoIP 国家代码（内置 Country 库即可生效）
  "cities": [                       // 城市级规则「国家/省/城市」，需启用 City 库
    "CN/GD/Shenzhen",
    "CN/*/Shenzhen",                // 省通配
    "*/GD/*"                        // 国家/城市通配
  ]
}
```

匹配语义：IP 规则、GeoIP 国家规则与城市规则取**并集**（任一命中即视为命中名单）。

| 模式 | 命中名单 | 未命中 |
|------|----------|--------|
| `blacklist`（默认） | 拒绝 | 放行 |
| `whitelist` | 放行 | 拒绝 |

> GeoIP 国家规则使用**编译期内置**的 GeoLite2-Country 数据库，无需任何额外配置即可使用；可用 `--geoip-db /path/to.mmdb` 指定外部数据库覆盖，或用 `--geoip-db none` 禁用。IP/IP 段规则始终可用。
>
> 连接被拒绝时会记录一条告警日志（含来源 IP）；**同一来源 IP 在 10 分钟内只提示一次**，避免被扫描时日志刷屏。

#### 城市级 GeoIP（Merged-IP）

内置数据库只精确到国家。如需按城市过滤，请在 **系统设置 → GeoIP 城市级数据库** 点击「下载城市数据库」（Merged-IP 城市/ASN 合并库，约 93MB，多镜像测速下载，保存到配置目录下的 `Merged-IP.mmdb`），下载完成后打开「启用城市级数据库」开关，**即时热加载、无需重启**；可随时再次点击「更新城市数据库」获取新数据。

- 下载地址（多镜像）：直连 GitHub Releases 与多个加速镜像，下载前会**并发测速（采样 1MB），自动选择最快的可用镜像**，失败时按速度顺序依次回退
  - 直连：`https://github.com/NetworkCats/Merged-IP-Data/releases/latest/download/Merged-IP.mmdb`
  - 也可用 `POST /api/geoip/download` 的 `url` 字段指定单一公网 HTTPS 地址（此时不做测速，单次下载上限为 256 MiB）
- 旧版本缓存 `GeoLite2-City.mmdb` 会在启动时自动重命名为 `Merged-IP.mmdb`，无需重新下载
- 城市规则在配置页通过**三级下拉**选取（国家 → 省/州 → 城市），选「全部（*）」表示通配；下拉数据由城市库自动生成（服务端首次扫描约需数十秒，之后使用本地缓存秒开）
- 规则存储格式为 `国家/省/城市`（如 `CN/GD/Shenzhen`）；省可用 ISO 3166-2 代码（`GD`）或英文名（`Guangdong`），城市使用英文名（`Shenzhen`），匹配不区分大小写
- 关闭开关即回退到内置国家库（城市规则不再生效，国家与 IP 规则不受影响）
- 已启用但城市库文件缺失/损坏时，启动会自动回退内置国家库并记录告警
- 也可用 `--geoip-db` 显式指定任意 Country / City 类型的 `.mmdb` 文件

### 代理端口连通性测试

配置页每个代理监听器下方有「测试代理端口」按钮：点击后后端会**经该监听端口**（`127.0.0.1:<监听端口>`）访问 `https://api.ip.sb/ip`，并在按钮旁显示出口 IP 与耗时；失败时显示具体原因（如端口未监听、认证失败、隧道不通）。

- 需先**启动隧道**（监听器处于运行状态）才能测试成功
- 支持四种协议与认证；HTTPS 代理的自签证书会被接受（隧道内目标证书仍正常校验）
- 出于安全考虑，仅允许探测本机回环上的代理端口，目标地址固定为 `https://api.ip.sb/ip`（可自定义 http/https URL）

---

## WEB API 文档

### 公开端点

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/auth/status` | 查询鉴权状态（是否启用密码、TLS） |
| POST | `/api/login` | 登录（仅密码模式注册） |

### 受保护端点

密码模式需在请求头携带 `Authorization: Bearer <token>`：

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/configs` | 列出所有配置 |
| POST | `/api/configs` | 新建配置 |
| PUT | `/api/configs/{id}` | 更新配置 |
| DELETE | `/api/configs/{id}` | 删除配置 |
| POST | `/api/configs/{id}/start` | 启动隧道 |
| POST | `/api/configs/{id}/stop` | 停止隧道 |
| POST | `/api/tunnels/start-all` | 批量启动 |
| POST | `/api/tunnels/stop-all` | 批量停止 |
| GET | `/api/tunnels/status` | 查询所有隧道状态 |
| GET | `/api/tunnels/stats` | 查询隧道连接与流量统计（含已停止隧道的进程内累计值，按监听器/本地转发细分） |
| GET | `/api/node/status?history_limit=N` | 查询节点运行状态：概览、当前活动连接、隧道流量与访问历史（默认 200 条，上限 1000） |
| GET | `/api/node/history?tunnel_id=ID&limit=N` | 查询单个节点（隧道）最近的访问历史（默认 500 条，上限 1000） |
| GET | `/api/metrics?range=1h&tunnel_id=ID` | 趋势数据（1h/6h/24h，降采样至约 120 点；`tunnel_id` 为空表示全局） |
| GET / POST / DELETE | `/api/alerts/rules` | 查询 / 保存 / 删除告警规则（DELETE 携带 `?id=`） |
| GET | `/api/alerts/history?limit=N` | 最近告警历史 |
| GET / POST | `/api/alerts/webhook` | 查询 / 保存 Webhook 通知配置 |
| POST | `/api/alerts/test` | 发送测试通知 |
| GET | `/api/history/search?tunnel_id=&from=&to=&result=&limit=&offset=` | 检索持久化访问历史（时间倒序；`result` 取 success/failed/denied；上限 1000） |
| GET | `/api/history/export?tunnel_id=&from=&to=&result=` | 导出访问历史 CSV 文本（上限 50000 条） |
| GET | `/api/logs?limit=N` | 获取最近 N 条日志 |
| POST | `/api/port/check` | 检测端口可用性 |
| POST | `/api/cert/generate` | 生成 HTTPS 自签证书（返回 `cert_pem` / `key_pem` 文本） |
| POST | `/api/cert/inspect` | 解析证书 PEM（body `{"cert_pem": "..."}`），返回使用者 CN、颁发者 CN、生效/到期时间、剩余天数与是否过期 |
| POST | `/api/hopchain/parse` | 解析跳板链简写 |
| POST | `/api/tunnel/test` | 测试隧道连通性（不启动监听） |
| POST | `/api/proxy/test` | 测试代理监听端口连通性，经该端口访问 `https://api.ip.sb/ip` 并返回出口 IP |
| GET | `/api/version` | 查询当前版本信息 |
| GET | `/api/version/check` | 检查 GitHub Release 新版本 |
| GET | `/api/auth/settings` | 查询访问密码设置（是否开启、用户名） |
| PUT | `/api/auth/settings` | 开启/关闭访问密码并设置凭据（开启成功返回 token） |
| GET | `/api/geoip/status` | 查询 GeoIP 状态（当前库类型、来源、大小、是否已下载城市库、是否启用） |
| GET | `/api/geoip/cities?country=CN` | 查询指定国家的省/城市下拉选项（首次调用后台构建索引，就绪前返回 `ready=false`） |
| POST | `/api/geoip/download` | 下载/更新城市级数据库（Merged-IP，可选 body `{"url": "..."}` 指定地址），完成后热加载 |
| PUT | `/api/geoip/settings` | 启用/关闭城市级数据库（body `{"enabled": true}`），启用前校验文件已下载 |
| GET | `/api/web/tls` | 查询 WEB 面板 HTTPS 设置（含当前证书/私钥内容与 certd 部署 Token） |
| PUT | `/api/web/tls` | 保存 HTTPS 设置（body 可用 `cert_pem`/`key_pem` 直接提交证书内容），保存后热重启监听 |
| POST | `/api/web/tls/token` | 重置 certd 部署 Token（旧 Token 立即失效） |

#### 证书部署 API（certd 等外部工具）

以下端点使用**独立 Token 鉴权**（与 WEB 登录密码开关无关），请求头携带 `X-API-Token: <部署 Token>`
（也支持 `Authorization: Bearer <Token>`）。Token 可在「系统设置 → WEB 面板 HTTPS」中查看或重置。

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/cert/targets` | 列出可部署目标：WEB 面板与所有 HTTPS 代理监听器（含是否已配置证书、隧道运行状态） |
| GET | `/api/cert/read?target=web` | 读取 WEB 面板证书与私钥 PEM 内容 |
| GET | `/api/cert/read?target=tunnel&tunnel_id=X&listener_id=Y` | 读取指定 HTTPS 监听器的证书与私钥 PEM 内容 |
| POST | `/api/cert/deploy` | 部署证书内容；`target=web` 写面板证书并热重启监听，`target=tunnel` 将 PEM 文本写入监听器配置并按需重启隧道 |

`POST /api/cert/deploy` 请求示例：

```jsonc
{
  "target": "tunnel",        // web | tunnel
  "tunnel_id": "xxx",        // target=tunnel 时必填
  "listener_id": "yyy",      // target=tunnel 时必填（HTTPS 监听器）
  "cert_pem": "-----BEGIN CERTIFICATE-----\n...",
  "key_pem": "-----BEGIN PRIVATE KEY-----\n...",
  "restart": true,           // 隧道运行中时是否重启以加载新证书（默认 true）
  "enable": true             // 仅 target=web：是否同时开启面板 HTTPS（默认 true）
}
```

```bash
curl -k -X POST https://127.0.0.1:8080/api/cert/deploy \
  -H "X-API-Token: <部署 Token>" \
  -H "Content-Type: application/json" \
  -d "{\"target\":\"web\",\"cert_pem\":\"$(cat fullchain.pem | sed ':a;N;$!ba;s/\n/\\n/g')\",\"key_pem\":\"$(cat privkey.pem | sed ':a;N;$!ba;s/\n/\\n/g')\"}"
```

> 面板证书内容写入 `<配置目录>/sshtunnel/certs/pair-*/web.crt|web.key`（私钥 0600）；隧道 HTTPS 监听器的证书**直接以 PEM 文本保存在 `configs.json` 的 `tls.cert_pem / tls.key_pem` 中**，部署后可在配置页直接查看与编辑（启动时可迁移配置目录 `certs` 内有效的旧版证书对。其他旧配置需重新填写 PEM）。

### WebSocket 端点

密码模式下通过 `?token=<jwt>` 携带鉴权：

| 路径 | 说明 |
|------|------|
| `/api/logs/stream` | 日志实时流（消息 `type: "log"`） |
| `/api/tunnels/status/stream` | 隧道状态实时流（消息 `type: "status"`） |

### 统一响应格式

```json
{
  "code": 0,
  "message": "success",
  "data": { }
}
```

- `code=0` 表示成功，非 0 为业务错误码
- HTTP 状态码与业务码类别对应（`code >= 40000` → 4xx，`code >= 50000` → 5xx）

---

## 项目结构

```
SSHTunnel/
├── main.go                      # 程序入口、CLI 参数解析、运行模式分发
├── app.go                       # Wails 桌面端绑定层（App 方法）
├── ssh_helper.go                # SSH 辅助函数
├── go.mod / go.sum              # Go 模块定义
├── wails.json                   # Wails 构建配置
├── install.sh                    # 根目录 systemd 服务安装脚本（生产部署）
├── README.md / LICENSE
├── build/                        # Wails 打包资源
│   ├── appicon.png
│   └── windows/                 # Windows 版本信息与清单
├── internal/                    # 业务逻辑层（8 个包）
│   ├── config/                  # 配置管理（持久化、CRUD）
│   ├── logger/                  # 日志（环形缓冲 + 多订阅者 sink）
│   ├── port/                    # 端口检测
│   ├── proxy/                   # 代理协议实现（HTTP/HTTPS/SOCKS4/SOCKS5）
│   ├── sshclient/               # SSH 客户端与跳板链解析
│   ├── tunnel/                  # 隧道管理（启停、自动重连、状态回调）
│   ├── updater/                 # GitHub Release 版本检测
│   └── web/                     # WEB 服务器、API、WebSocket、JWT 鉴权
├── frontend/                    # Vue 3 + Element Plus 前端
│   ├── src/
│   │   ├── api/                 # 双模式 API 适配层（Wails Call / HTTP fetch）
│   │   ├── components/           # HopEditor、ProxyListenerEditor
│   │   ├── router/              # 路由守卫（WEB 模式登录拦截）
│   │   ├── stores/              # Pinia 状态管理
│   │   ├── views/               # Dashboard / ConfigEdit / Logs / Login / Settings
│   │   └── types.ts             # 与 Go 结构体一一对应的 TS 类型
│   ├── wailsjs/                 # Wails 自动生成的 JS 绑定
│   └── dist/                    # 构建产物（go:embed 嵌入到二进制）
├── scripts/
│   └── install.sh               # GitHub Release 下载安装脚本
└── releases/                   # 预编译二进制（提交到仓库）
```

---

## 代码说明

### 后端包职责

#### `internal/config` — 配置管理

隧道配置的持久化管理。读写 `configs.json` 文件，运行时状态字段不持久化。

- **核心类型**：`TunnelConfig`、`HopConfig`、`LocalForward`、`ProxyListener`、`Manager`
- **设计要点**：
  - `sync.RWMutex` 保护并发访问
  - `Save` 时自动生成 UUID 并回填 ID，存储副本避免外部修改
  - `userConfigDir` 为包级变量，可被测试覆盖

#### `internal/logger` — 日志系统

多目标日志记录：文件 + 环形缓冲 + 实时 sink 推送。

- **核心类型**：`Level`、`Entry`、`Logger`
- **设计要点**：
  - 环形缓冲（容量 1000 条），通过 `Recent(limit)` 查询历史
  - `AddSink` 注册回调，sink 异步推送到前端（Wails `EventsEmit` / WebSocket 广播）
  - `NewTest()` 提供无磁盘 I/O 的测试实例
  - 日志文件按日切割：`<UserCacheDir>/sshtunnel/logs/sshtunnel-YYYY-MM-DD.log`

#### `internal/port` — 端口检测

纯函数式工具包，无状态。

- `IsAvailable(port)` — 检测端口是否可用
- `BindAddress(port, allowExternal)` — 返回 `0.0.0.0` 或 `127.0.0.1`
- `FindAvailable(start)` — 从指定端口起扫描可用端口（最多 100 个）

#### `internal/sshclient` — SSH 客户端

SSH 多跳串联拨号与隧道 Dial 复用。

- **核心类型**：`Hop`、`Client`
- **设计要点**：
  - `ParseHopChain(s)` 支持 `,` 与 `->` 两种分隔符，默认端口 22
  - 多跳串联：第一跳 `ssh.Dial`，后续跳通过前一跳 `client.Dial` + `ssh.NewClientConn` 逐跳建立
  - 支持 password 与 key（含 passphrase）两种认证
  - 依赖 `golang.org/x/crypto/ssh`

#### `internal/proxy` — 代理协议

多协议代理服务器实现与统一管理。

- **核心接口**：`Server`（`Start/Stop/Status/Protocol/ListenAddr/ID`）、`Dialer`
- **文件分工**：
  - `proxy.go` — `Server` 接口、`BaseServer` 公共结构、`BuildServer` 工厂方法
  - `manager.go` — 多监听器生命周期管理
  - `http_proxy.go` — HTTP/HTTPS 代理（CONNECT 隧道 + Basic Auth + TLS）
  - `socks5.go` — SOCKS5 实现（RFC 1928/1929）
  - `socks4.go` — SOCKS4/4a 实现（UserID 认证 + 域名直连）
  - `cert.go` — ECDSA P-256 自签证书生成（10 年有效期）
  - `auth.go` — 统一认证辅助
  - `firewall.go` — 简易防火墙（IP/国家/城市规则匹配）
  - `geoip.go` — GeoIP 数据库管理（内置 Country 库、城市库下载与热加载、设置持久化）
  - `cityindex.go` — 城市下拉索引（扫描 City 库生成国家/省/城市列表，带磁盘缓存）
  - `util.go` — `Bridge` 双向桥接
- **设计要点**：
  - 接口抽象 + `BaseServer` 嵌入复用，`BuildServer` 工厂按协议分发
  - 编译期接口断言 `var _ Server = (*httpServer)(nil)`

#### `internal/tunnel` — 隧道管理

单条隧道的运行实例与多隧道管理。

- **核心类型**：`Tunnel`、`Manager`
- **设计要点**：
  - **接口抽象 + 依赖注入**：内部定义 `tunnelClient` 与 `sshDialer` 接口，`*sshclient.Client` 隐式满足；包级 `dialer` 变量可被测试替换
  - **指数退避自动重连**：`watchAndReconnect`（初始 2s，上限 60s，默认无限重试）
  - **资源回收一致性**：`cleanupResources` 先锁内快照清空字段，再锁外阻塞 Close，避免数据竞争
  - `OnStatusChange` 支持多订阅者（拷贝回调列表后触发）

#### `internal/updater` — 版本检测

GitHub Release 版本检测与二进制资产定位。

- **核心类型**：`Release`、`Asset`
- **设计要点**：
  - `httpClient` 为包级变量（超时 15s），可被测试替换
  - 资产命名：web 变体 `sshtunnel-{os}-{arch}`，desktop 变体加 `-desktop` 后缀
  - 版本比较支持 `v/V` 前缀剥离、pre-release 后缀忽略、缺位补 0
  - `DefaultRepo = "xuanlove/SSHTunnel"`

#### `internal/web` — WEB 服务器

WEB API 服务器 + JWT 鉴权 + WebSocket 实时推送 + 嵌入式前端 SPA 托管。

- **核心类型**：`Config`、`Server`、`Handler`、`Hub`、`WSMessage`
- **文件分工**：
  - `server.go` — HTTP 服务器、路由树、TLS 热重启、SPA fallback、`writeJSON/writeError`
  - `handlers.go` — API 路由（configs / tunnels / logs / version / web tls 等）
  - `auth.go` — `authMiddleware`、JWT 签发/校验（HS256，24h 过期）
  - `authstore.go` — 访问密码与 JWT 密钥持久化
  - `websettings.go` — 面板 HTTPS 设置、证书 PEM 落盘、certd 部署 Token
  - `certapi.go` — certd 证书部署 API（列出目标、读取、部署并重启隧道）
  - `ws.go` — `Hub` 广播器 + WebSocket 端点
- **设计要点**：
  - **embed.FS**：`//go:embed all:assets` 嵌入前端编译产物
  - **sync.Once**：`handleLogStream` 用单一 cleanup 闭包统一回收资源（读循环、心跳 goroutine、广播路径并发安全）
  - 30s 心跳 Ping 保活 WebSocket
  - 业务码到 HTTP status 映射

### 前端结构

#### 双模式 API 适配层（`src/api/index.ts`）

通过 `isWeb = !window.go` 检测运行环境：

- **桌面端**（Wails WebView 注入了 `window.go`）：用 `Call.ByName('App.Xxx', ...)` 调用 Go 绑定方法 + `Events.On` 订阅事件
- **浏览器端**：用 `fetch` 调 `/api/*` 端点 + WebSocket 连接实时流
- Token 管理：`setToken/getToken` + localStorage 持久化（仅 WEB 模式），401 自动跳转登录页

#### 视图（`src/views/`）

| 视图 | 说明 |
|------|------|
| `Dashboard.vue` | 隧道列表总览，跳板链/监听器摘要，启动/停止/删除/批量操作 |
| `ConfigEdit.vue` | 隧道配置编辑表单，嵌入 HopEditor 与 ProxyListenerEditor，支持连接测试 |
| `Login.vue` | WEB 模式登录页 |
| `Logs.vue` | 日志查看器，按级别与关键字过滤，彩色标签 |
| `Settings.vue` | 系统设置（面板 HTTPS、GeoIP 城市库、端口检测、自签证书生成、版本信息） |

#### 组件（`src/components/`）

| 组件 | 说明 |
|------|------|
| `HopEditor.vue` | 跳板链编辑器，支持简写格式解析，增删跳，合并保留已有认证信息 |
| `ProxyListenerEditor.vue` | 代理监听器编辑器，协议选择、端口/认证/TLS 配置 |

#### 状态管理（`src/stores/tunnel.ts`）

Pinia store 封装 `configs`/`logs`/`loading` 状态与所有异步操作，日志超 1000 条时自动裁剪到 500。

#### 路由（`src/router/index.ts`）

`createWebHashHistory`，5 条路由。`beforeEach` 守卫在 WEB 模式下查询 `/api/auth/status`，密码模式无 token 跳转登录页；桌面端直接放行。

---

## 开发规范

### 代码组织

- **后端**：所有业务逻辑放在 `internal/` 下，按职责分包，每个包可独立测试
- **前端**：Vue 3 `<script setup lang="ts">` + Composition API + Pinia
- **类型同步**：`frontend/src/types.ts` 与 `internal/config/config.go` 的结构体一一对应，修改后端字段需同步更新前端类型

### 编码规范

- Go 代码遵循 [Effective Go](https://go.dev/doc/effective_go) 与 [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)
- 包注释以 `// Package xxx` 开头，导出符号需有文档注释
- 接口优先：对外暴露接口而非具体类型（如 `proxy.Server`、`tunnel` 的 `tunnelClient`/`sshDialer`）
- 锁使用：
  - `sync.RWMutex` 用于读多写少场景（如 `config.Manager`）
  - `sync.Mutex` 用于一般互斥（如 `logger`、`tunnel`）
  - 资源回收遵循"先锁内快照清空，再锁外阻塞释放"模式，避免死锁与数据竞争
- 并发安全：所有跨 goroutine 共享的状态必须受锁保护，回调触发时拷贝列表后执行

### 前端规范

- 使用 TypeScript 严格模式（`vue-tsc --noEmit` 在构建前进行类型检查）
- API 调用统一通过 `src/api/index.ts`，不直接在组件中写 fetch
- 状态管理通过 Pinia store，组件不直接持有业务状态
- Element Plus 组件按需引入，图标全局注册

### 测试规范

每个包应包含 `_test.go` 文件，测试覆盖：

| 包 | 测试覆盖 |
|------|------|
| `config` | CRUD、ID 回填、状态默认值、持久化重载 |
| `logger` | 格式化、TunnelID 作用域、sink 回调、环形缓冲溢出 |
| `port` | `IsAvailable`、`BindAddress`、`FindAvailable` |
| `sshclient` | `ParseHopChain` 全场景（单跳/多跳/默认端口/箭头分隔/IPv6） |
| `tunnel` | mock SSH 客户端、启停、自动重连（指数退避）、多订阅者状态回调 |
| `proxy` | echo 服务器端到端：SOCKS5/HTTP CONNECT/SOCKS4、认证、端口冲突 |
| `updater` | 版本比较（22 组用例）、资产命名、httptest 模拟 GitHub API |
| `web` | 鉴权中间件、登录、splitPath |

运行测试：

```bash
# 全部后端测试
go test ./...

# 带竞态检测（CI 使用）
go test -race ./...

# 指定包
go test ./internal/tunnel/...
```

前端测试（vitest + @vue/test-utils + happy-dom）：

| 文件 | 覆盖 |
|------|------|
| `src/utils/format.spec.ts` | 字节数/时长/本地时间格式化、时间范围换算的边界值与容错 |
| `src/api/index.spec.ts` | web（fetch mock：鉴权头、401 幂等跳转、错误分支）/ desktop（`window.go` 绑定 stub）双模式 |
| `src/components/TrendChart.spec.ts` | 空数据占位、折线/面积路径渲染、指标切换重算 |
| `src/views/Status.spec.ts` | 概览/节点卡片渲染、告警横幅规则去重、节点抽屉与 24h 历史检索、节点删除关闭抽屉 |

```bash
cd frontend

# 单次运行
npm test

# 监听模式
npm run test:watch

# 覆盖率报告（CI 使用；对 utils/api/TrendChart 设阈值，未达标即失败）
npm run test:coverage
```

### 提交规范

提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)：

```
<type>: <description>

- 要点 1
- 要点 2
```

常用 type：`feat`（新功能）、`fix`（修复）、`refactor`（重构）、`docs`（文档）、`build`（构建）、`test`（测试）。

---

## 构建与发布

### 版本号规范

遵循 [语义化版本](https://semver.org/lang/zh-CN/)：`MAJOR.MINOR.PATCH`

- `MAJOR`：不兼容的 API 变更
- `MINOR`：向后兼容的功能新增
- `PATCH`：向后兼容的缺陷修复

### 版本注入

版本信息通过 `-ldflags` 在构建时注入到 `main.go` 的包级变量：

```bash
VERSION=v1.1.0
COMMIT=$(git rev-parse --short HEAD)
BUILDTIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# web 变体
LDFLAGS_WEB="-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILDTIME} -X main.buildVariant=web"

# desktop 变体
LDFLAGS_DESKTOP="-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=${BUILDTIME} -X main.buildVariant=desktop"
```

未注入时默认值：`version=dev`、`commit=none`、`buildTime=unknown`、`buildVariant=web`。

### 交叉编译全平台

#### WEB 变体（纯 Go，CGO 禁用）

```bash
# Linux amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "${LDFLAGS_WEB}" -o releases/sshtunnel-linux-amd64 .

# Linux arm64
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "${LDFLAGS_WEB}" -o releases/sshtunnel-linux-arm64 .

# macOS amd64 (Intel)
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "${LDFLAGS_WEB}" -o releases/sshtunnel-darwin-amd64 .

# macOS arm64 (Apple Silicon)
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "${LDFLAGS_WEB}" -o releases/sshtunnel-darwin-arm64 .

# Windows amd64
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "${LDFLAGS_WEB}" -o releases/sshtunnel-windows-amd64.exe .
```

#### Desktop 变体（含 WebView，CGO 启用）

```bash
# Windows amd64 桌面版（Linux 宿主用 mingw-w64 交叉编译）
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
  go build -ldflags "${LDFLAGS_DESKTOP} -H=windowsgui" \
  -o releases/sshtunnel-windows-amd64-desktop.exe .

# Linux amd64 桌面版（需在 Linux 宿主编译）
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build -ldflags "${LDFLAGS_DESKTOP}" -o releases/sshtunnel-linux-amd64-desktop .

# macOS 桌面版（在 macOS 原生构建；需额外链接 UniformTypeIdentifiers 框架）
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
  CGO_LDFLAGS="-framework UniformTypeIdentifiers" \
  go build -tags "desktop,production" -ldflags "${LDFLAGS_DESKTOP}" \
  -o releases/sshtunnel-darwin-arm64-desktop .
```

> `-H=windowsgui` 用于 Windows 桌面版，隐藏控制台窗口（GUI 子系统）。
> macOS 桌面版也可通过工作流自动构建：更新 [.github/macos-build-trigger](.github/macos-build-trigger) 触发 Actions，构建产物会自动上传到最新 tag 对应的 Release。

### 资产命名规则

| 变体 | 命名规则 | 示例 |
|------|----------|------|
| web（默认） | `sshtunnel-{os}-{arch}[.exe]` | `sshtunnel-linux-amd64` |
| desktop | `sshtunnel-{os}-{arch}-desktop[.exe]` | `sshtunnel-windows-amd64-desktop.exe` |

`--check-update` 与安装脚本会按当前二进制的 `buildVariant` 匹配对应资产。

### 发布校验和

安装脚本会校验下载资产的 sha256（资产旁附带同名 `.sha256` 文件）。发布前生成：

```bash
for f in releases/sshtunnel-*; do
  case "$f" in *.sha256) continue;; esac
  sha256sum "$f" > "$f.sha256"
done
```

上传 Release 时将二进制与对应 `.sha256` 一并上传；未提供校验和的旧版本会在安装时提示并跳过。

### 前端构建

```bash
cd frontend
npm install        # 安装依赖
npm run dev        # 开发模式（Vite dev server，Wails 自动探测）
npm run build      # 生产构建（vue-tsc 类型检查 + vite build → dist/）
npm run preview    # 预览构建产物
```

构建产物位于 `frontend/dist/`，通过 `go:embed` 嵌入到后端二进制（`main.go` 与 `internal/web/server.go` 各嵌入一份）。

### 发布流程

1. 更新版本号（如 `v1.1.0`）
2. 交叉编译所有平台二进制到 `releases/`
3. 提交代码并推送到 `main` 分支
4. 打 tag 并推送：`git tag v1.1.0 && git push origin v1.1.0`
5. 创建 GitHub Release 并上传所有二进制资产

---

## 安全建议

1. **WEB 面板监听非回环地址时必须启用访问密码**。远程部署使用 HTTPS 或经 HTTPS 反向代理访问
2. **生产环境推荐启用 HTTPS**：在「系统设置」中开启面板 HTTPS 或使用 `--tls-cert` / `--tls-key`，防止 Token 被中间人窃取
3. **SSH 密钥以 PEM 文本存储于配置文件**：请妥善保护 `configs.json`，避免提交到版本控制系统
4. **JWT 密钥持久化于 `settings.json`**：服务重启/升级后已签发的 Token 仍然有效；如需强制所有会话重新登录，删除该文件中的 `jwt_secret` 字段并重启服务
5. **certd 部署 Token 等同于证书写入权限**：请仅在可信网络中开放部署 API，泄露后在「系统设置」中重置 Token 即可立即失效
6. **systemd 部署已内置安全加固**：`ProtectSystem=strict`、`PrivateTmp=true`、`NoNewPrivileges=true`、专用 `nologin` 系统用户

---

## 版本更新说明

### v1.9.7

- **账户配置生成器新增「生效校验」**：写入 sshd 配置并 reload 后，自动执行 `sshd -T -C user=<账户>,host=...,addr=...` 检查实际生效的转发配置。sshd 对同一参数以第一个匹配值为准，若全局更早设置了 `AllowTcpForwarding no` 或 `DisableForwarding yes`，文末的 `Match` 块会被静默忽略——此前表现为隧道能连上但 SOCKS/转发全部失败（浏览器常见 `ERR_CONNECTION_RESET`），现在会直接打印原因与修复指引
- **失败原因进入访问历史**：SOCKS4/SOCKS5、HTTP/HTTPS 代理连接目标失败时，访问历史记录会附带底层错误（如 `ssh: rejected: administratively prohibited`），便于在「节点状态 → 访问历史」直接定位问题
- **测试**：校验片段覆盖 `yes/no`、`no`、`disableforwarding=yes`、`local`、无输出五类分支；前端 93 用例、`vue-tsc`、Go 代理包 `-race` 通过

### v1.9.6

- **修复隧道卡片随机排序**：隧道配置此前保存在 Go map 中，`List()` 与持久化均遍历 map，导致仪表盘「隧道管理」与「节点状态」的节点卡片每次刷新顺序都可能变化。现改为维护显式创建顺序：新建追加到末尾、编辑保持位置、删除仅移除自身，`configs.json` 也按同一顺序写入，重启后位置固定
- **测试**：新增顺序稳定性用例（20 轮读取顺序一致、编辑不改变位置、删除后相对顺序不变、重新加载顺序不变），Go 全量用例通过

### v1.9.5

- **修复升级模式版本误报**：`install.sh -u` 原先通过 `--version | head -1` 解析已安装版本，管道提前关闭会使 Go 程序收到 SIGPIPE，在 `pipefail` 下被判失败并显示「当前已安装 未知」；现改为完整捕获输出后解析，无法读取时输出明确警告（含二进制路径）而非「未知」
- **升级后自动重启服务**：升级模式由 `try-restart` 改为 `restart`，已安装的 systemd 服务即使当前未运行也会被拉起；新增重启结果检查（成功提示新版本已生效，失败给出 `journalctl` 排查指引，未安装服务时明确跳过）
- **测试**：`shellcheck` 零告警；版本解析覆盖正常/空输出/异常格式/执行失败；升级流程用 stub `systemctl` 验证「已安装并运行」「未安装」「重启后未运行」三条分支

### v1.9.4

- **安装脚本多镜像 + 测速优选**：内置 GitHub 直连与 `ghfast.top`、`ghproxy.net`、`gh-proxy.com`、`github.moeyy.xyz` 等代理，并行对每个源做 512KB Range 采样，按实测吞吐排序后从最快源下载，失败（含代理返回 HTML 错误页）自动切换下一个源；获取最新版本号同样支持多源重试
- **镜像可配置**：新增 `SSHTUNNEL_MIRRORS`（追加镜像根）、`SSHTUNNEL_DIRECT=0`（只用镜像）、`SSHTUNNEL_MIRROR_TEST=0`（跳过测速按顺序尝试）三个环境变量
- **主机密钥指纹改为可选**：跳板配置中的 `host_key` 不再强制填写，留空时沿用「不校验主机密钥」的行为并输出一次安全提示；填写后仍严格比对 SHA256 指纹以防中间人。原先「每一跳必填」的保存校验一并移除
- **测试**：安装脚本通过 `shellcheck` 与本地多镜像模拟（快/慢/HTML 错误页）端到端验证；Go 全量用例与前端 93 用例通过

### v1.9.3

- **修复 SOCKS 代理 IPv6 目标连接失败**：部分客户端将 IPv6 目标以带方括号的域名下发（如 `[2600:1f18::1]`），此前会再套一层括号得到 `[[2600:1f18::1]]:443`，拨号报 `missing port in address`；现统一剥掉一层包裹方括号后再拼接（SOCKS5 域名/IPv6 与 SOCKS4a 域名均生效）
- **测试**：新增 3 个回归用例（SOCKS5 方括号域名 IPv6、SOCKS5 IPv6 ATYP、SOCKS4a 方括号域名 IPv6），代理包全量用例通过

### v1.9.2

全库代码审查修复版本：3 项严重、7 项较高风险与 13 项一般问题，并新增主机密钥校验与键盘交互认证。

- **修复僵尸隧道**：未启用自动重连的隧道断线后，本地转发与代理监听器现在会正确关闭并释放端口，重启隧道不再因端口占用失败
- **修复纯 HTTP 代理**：绝对形式请求目标省略默认端口（如 `GET http://example.com/`）时自动补 80，不再一律 502
- **修复任意文件读取**：WEB 面板 TLS 设置接口对显式指定的证书/私钥路径做 PEM 有效性校验（无论是否启用 HTTPS），非 PEM 文件不再可被持久化并经回读泄露
- **主机密钥校验**。每跳连接都需要可信的 `host_key`，支持 `SHA256:` 指纹、Base64 公钥或 OpenSSH 公钥行
- **键盘交互认证（新增）**：密码认证自动追加 keyboard-interactive 回退（以相同密码应答挑战），兼容「禁用密码认证但保留键盘交互」的服务器；认证被拒的错误信息附带排查提示
- **后端加固**：消除 SSH 连接 context 竞态；HTTP 代理请求头 64KB 有界预读防内存放大；`/api/login` 请求体限制；面板认证设置原子写入（断电不再静默关闭访问密码）；GeoIP 下载 SSRF 防护（拒绝回环/私网地址，错误详情不回显）；SOCKS 认证常量时间比较、日志注入防护与 RFC 1929 子协商校验；拒绝日志与登录限流记录有界化；访问历史倒序读取内存有界且写入失败可观测；WEB 启动失败进程快速退出；日志不再按条派生 goroutine
- **前端**：TLS 私钥默认不回显（`has_key`/`key_masked`，显式「查看」才拉取明文）；CSV 导出公式注入防护；开启访问密码强制非空且 ≥6 位；三处定时器卸载清理；401 重登后自动刷新列表；复制命令统一 Clipboard/execCommand 降级；端口输入防 NaN
- **测试**：新增 20+ 用例（主机密钥校验、有界请求头、SSRF 拒绝、私钥掩码、限流过期、JSONL 有界读取等），Go 10 包全绿，前端 92 用例全过

### v1.9.1

- **认证方式可选**：账户配置生成器支持「公钥认证（推荐）/ 密码认证」，默认跟随第 1 跳的认证方式，手动切换后不再覆盖
- **密码模式**：生成 `useradd` + 交互式 `sudo passwd` + sshd 限制命令，密码不写入命令或剪贴板；同时提示暴力破解风险与 `AuthenticationMethods` 适配
- **配置写入可重复执行**：sshd `Match` 块使用标记块替换，重复执行或切换认证方式不再产生冲突；通过临时文件 + `&&` 链写入，解析/读取失败时不会覆盖原配置
- **复制命令同校验**：复制与生成共用校验，非法用户名/公钥中止操作，公钥占位符给出明确警告
- **测试**：HopEditor 组件用例 15 个，前端用例合计 76 个

### v1.9.0

- **最小权限 SSH 账户引导**：跳板链编辑器「+ 添加跳板」下方新增可折叠示例，提供无登录 Shell、仅密钥、仅开放 TCP 转发的账户创建命令，含 `Match User` 转发限制与 `sshd -t` 校验；`command -v nologin` 兼容 Debian 与 RHEL
- **一键生成配置**：填写用户名（默认跟随第 1 跳）与可选 `.pub` 公钥后，一键生成完整命令并复制到剪贴板；HTTP 环境无 Clipboard API 时自动降级
- **安全校验**：用户名限定小写字母/数字/下划线/短横线；公钥需符合「类型 + base64 + 可选注释」，命令中单引号包裹并拒绝引号/换行，避免命令注入；未填公钥时明确提示占位符
- **测试**：新增 HopEditor 组件用例（追加跳板、跟随/停止跟随、生成、非法输入、占位符警告、复制降级），前端用例合计 71 个

### v1.8.1

- **边界修复**：`rangeToFromISO` 使用自有属性校验，`constructor`/`toString`/`__proto__` 等原型链键按未知范围返回空串，不再抛 `RangeError`
- **测试增强**：用例 45 → 61，新增关键 API 封装（`checkAuthStatus` 双模式、`httpPost` 401、`saveConfig` POST/PUT、`updateWebAuthSettings` token 落库与清除、`geoIPAvailable` 降级、桌面端事件订阅）、CSV 导出下载、趋势标签加载、历史筛选切换、键盘打开抽屉
- **覆盖率门槛接入 CI**：`utils` 100%，`api` 与 `TrendChart` 设渐进阈值，未达标构建失败；覆盖率报告作为 CI 产物保留 7 天
- **工程修正**：覆盖率统计范围收敛到 `src/**`（排除生成代码）；`vitest.config.ts` 复用 `vite.config.ts` 并纳入类型检查；忽略 `frontend/coverage/`
- 本版本不改变产品运行时行为

### v1.8.0

- **前端测试基建**：引入 vitest + @vue/test-utils + happy-dom，建立与后端对齐的前端自动化测试防线（45 个用例）
- **测试覆盖**：`utils/format` 纯函数边界值与容错（含从 Status.vue 抽出的时间范围换算 `rangeToFromISO`）；`api` 层 web/desktop 双模式分支（fetch mock：鉴权头、401 幂等跳转、业务错误；`window.go` Wails 绑定 stub）；`TrendChart` 空数据/路径渲染/指标切换；`Status` 视图告警横幅规则去重、节点抽屉历史检索与节点删除自动关闭
- **CI**：Test 工作流前端 job 在类型检查前执行 `npm test`
- 本版本不改变任何运行时行为

### v1.7.1

- **访问历史持久化**：访问记录异步写入按天 JSONL 分片（0600，保留 30 天，启动与每日自动清理过期分片），重启自动恢复最近 2000 条到内存环形缓冲；写入队列满时丢弃计数，不阻塞代理热路径
- **趋势数据持久化**：每 5 分钟将指标采样快照原子写入 `metrics-snapshot.json`，启动恢复；重启边界不计增量，避免虚假流量尖峰
- **告警历史持久化**：告警触发/恢复事件落盘，重启后保留最近 200 条
- **检索与导出**：新增 `GET /api/history/search`（按隧道/时间范围/结果过滤、分页）与 `GET /api/history/export`（CSV 文本，上限 50000 条）；节点详情「访问历史」标签新增时间范围与结果筛选器及 CSV 导出按钮
- **桌面端**：新增 `SearchAccessHistory` / `ExportAccessHistoryCSV` 绑定

### v1.7.0

- **流量趋势**：每 10 秒采样全局与各隧道的连接数、上下行增量，内存保留 24 小时；仪表盘新增全局趋势卡，节点详情新增「趋势」标签，支持 1h/6h/24h 切换（降采样约 120 点）
- **告警规则**：支持隧道断开（非主动停止）、连接数阈值、5 分钟流量阈值三类规则，可配置持续窗口与冷却时间，规则持久化到 `alerts.json`
- **通知**：Webhook 支持通用 JSON、企业微信、飞书三种格式，异步发送（5s 超时）不阻塞主流程；设置页可一键发送测试通知
- **告警历史**：内存保留最近 200 条，设置页展示最近告警，节点状态页顶部显示当前未恢复告警
- **接口**：新增 `/api/metrics`、`/api/alerts/rules`、`/api/alerts/history`、`/api/alerts/webhook`、`/api/alerts/test` 及对应桌面端绑定

### v1.6.0

- **日志轮转**：单文件上限 10MB，保留最近 5 份；日志目录 0700、文件 0600
- **WebSocket 慢客户端隔离**：每连接独立发送队列（256 条），队列满丢弃最旧消息，不再阻塞其他客户端；写与心跳串行
- **代理慢连接防护**：SOCKS4/5 握手 10s 超时、HTTP 请求头读取 15s 超时、TLS 握手 10s 超时、请求头 64KB 上限（超出返回 431）；桥接与响应写增加 30s 空闲写超时
- **城市规则 fail-fast**：配置城市规则但未加载城市级 GeoIP 数据库时，监听器启动失败并提示，避免黑名单静默失效
- **归档统计精度**：主动停止或重连耗尽后等待桥接连接收尾（最多 300ms）再归档
- **发布校验和**：每个发布资产附带同名 `.sha256`，安装脚本下载后校验，不匹配则中止
- **CI**：新增 push/PR 工作流，执行 `go vet`、`go test -race`、`vue-tsc` 与前端构建

### v1.5.0

- **卡片累计流量**：仪表盘隧道卡片明确展示「累计上传 / 累计下载」与活跃、累计连接数
- **累计值跨停止保留**：隧道停止或重连耗尽后，本进程内的连接数与流量汇总继续保留；再次启动后与新的运行阶段累加
- **新增「节点状态」页**：
  - 概览：运行时长、隧道运行/总数、监听服务与本地转发数、活跃/累计连接、累计上传/下载
  - 当前连接：实时列出每条连接的协议、隧道、监听端口、来源、目标、已持续时长与单连接上下行字节
  - 访问历史：结构化记录目标、来源、结果与耗时，沿用监听器「记录访问日志」开关；进程内保留最近 2000 条，支持跨隧道与停止后查看
  - 自动刷新（3 秒）可随时关闭，支持手动刷新
- **统计口径修正**：监听器构建失败（证书/参数错误）时不再出现在统计中

### v1.4.2

- **隧道状态实时同步**：前端订阅状态推送（Web 端 WebSocket / 桌面端事件），重连耗尽、断线等状态变化无需刷新页面即可更新卡片标签与操作按钮
- **重连窗口统计保持**：监听器元信息与累计统计在监听器暂停期间继续输出，重连过程中不再短暂显示 0 B / 0 连接
- **停止与重连耗尽竞态**：已主动停止的隧道不再被误报为「错误」状态
- **HTTP 认证失败拒绝日志**：拒绝访问日志补充已解析出的目标地址（如 `访问 example.com · 来源 1.2.3.4 · 拒绝: 认证失败`）

### v1.4.1

- **修复重连耗尽后状态卡在「运行中」**：达到 `max_reconnect_attempts` 后，隧道会从运行列表移除并将配置状态置为「错误」，仪表盘按钮恢复为「启动」，可直接重新启动
- **修复「累计」统计在重连后清零**：监听器与本地转发的连接数/流量统计对象在隧道生命周期内复用，SSH 断线重连后继续累计
- **修复编辑旧隧道静默开启心跳**：旧配置缺少 `keepalive_seconds` 时保持不变（关闭），仅新建隧道默认 60 秒
- **统一流量统计口径**：HTTP 代理仅统计请求体/响应体字节（不再包含协议头），与 SOCKS、本地转发一致
- **访问日志补充拒绝记录**：防火墙拦截与认证失败会记录来源地址与原因，同一来源同一原因 10 分钟内去重
- **界面与健壮性**：字节单位不显示小数；仪表盘统计轮询增加在途防抖；代理基础结构改为指针嵌入，`go vet` 检查通过
- **补充自动化测试**：HTTP 代理统计口径、本地转发统计（含重连后累计）、重连次数耗尽状态、心跳失败触发重连

### v1.4.0

- **运行统计（流量与连接数）**：新增 `GET /api/tunnels/stats`，按隧道与监听器/本地转发统计活跃连接、累计连接与上下行字节
  - 仪表盘每 3 秒轮询展示 `↑ 上行 / ↓ 下行 / 连接数`，监听器标签悬浮显示该监听器明细
  - 流量采用写入时实时累计，长连接也可持续看到增长
- **代理访问日志**：代理监听器新增「记录访问日志」开关（`access_log`）
  - 记录每次访问的目标地址、结果（成功/失败原因）与耗时，例如 `访问 example.com:443 · 成功 · 耗时 120ms`
  - 「操作日志」页新增隧道下拉过滤，可配合关键字与级别筛选后导出
  - 修复日志 WebSocket 在多个页面重复订阅导致日志重复的问题
- **SSH 心跳与可配置重连策略**
  - 新增 `keepalive_seconds`：定期发送 `keepalive@openssh.com`，心跳失败自动断开并触发重连（0 表示关闭）
  - 新增 `max_reconnect_attempts`（0 表示无限）与 `reconnect_max_delay_seconds`（0 表示默认 60）
  - 编辑页提供对应输入项，达到最大重连次数后停止并记录错误日志

### v1.3.10

- **修复登录后隧道列表为空**：刚登录进入仪表盘时隧道卡片不显示、需刷新页面才出现
  - 原因：应用启动时路由守卫尚未完成重定向到登录页，初始化逻辑误判为已加载并在无 Token 时请求配置（401）；登录成功后不再重新拉取
  - 现在仅在配置实际加载成功后标记完成，登录进入主界面会自动加载；日志 WebSocket 也只在加载成功后订阅
- **修复迟到 401 竞态**：登录前发出的请求若在登录成功后才返回 401，不再清除刚获得的新 Token 或把用户踢回登录页

### v1.3.9

- **HTTPS 证书摘要（隧道 + WEB 面板）**：证书内容下方显示证书使用者公用名（CN）与到期日期
  - 隧道 HTTPS 监听器配置页与「系统设置 → WEB 面板 HTTPS（SSL）」均支持，打开已有证书（含 certd 部署的证书）立即显示
  - 标签颜色随状态变化：正常为绿色、30 天内到期为橙色、已过期为红色，并显示剩余天数
  - 粘贴/修改证书内容后自动解析（400ms 防抖）
  - 生成自签证书后同样显示 CN 与到期日期
  - 新增 `POST /api/cert/inspect` 接口返回 `subject_cn / issuer_cn / not_before / not_after / dns_names / expired / days_left`
- **修复证书工具接口鉴权**：`/api/cert/generate` 与 `/api/cert/inspect` 不再被 certd 部署 Token 拦截，面板内可正常调用

### v1.3.8

- **隧道 HTTPS 证书改为 PEM 文本模式**：`proxy_listeners[].tls` 由 `cert_file / key_file` 文件路径改为 `cert_pem / key_pem` 文本
  - 配置页 HTTPS 监听器直接显示/编辑证书与私钥内容，可一键生成自签证书填入
  - certd 部署 API 将证书写入配置文本，部署后可在界面正常查看与修改
  - 兼容旧配置：启动时自动读取旧 `cert_file / key_file` 指向的文件内容并迁移为文本
  - `POST /api/cert/generate` 改为返回 `cert_pem / key_pem`；`GET /api/cert/targets` 使用 `cert_configured` 标识配置状态

### v1.3.7

- **城市库多镜像下载 + 测速选优**：内置 6 个下载地址（GitHub 直连 + 5 个加速镜像），下载前并发测速（采样 1MB，8 秒超时），自动选择最快的可用镜像
  - 测速失败的镜像自动跳过，下载失败时按速度顺序依次回退
  - 测速结果写入服务日志，便于排查网络问题
  - 仍可通过 `POST /api/geoip/download` 的 `url` 字段指定单一地址（此时不做测速）

### v1.3.6

- **城市级数据库来源改为 Merged-IP**：`https://github.com/NetworkCats/Merged-IP-Data/releases/latest/download/Merged-IP.mmdb`（城市 + ASN 合并库，约 93MB）
  - 缓存文件改为 `sshtunnel/Merged-IP.mmdb`；旧版 `GeoLite2-City.mmdb` 会自动重命名迁移，无需重新下载
  - 改用 `maxminddb` 直接读取，兼容 `Merged-IP-City-ASN` 等非 GeoLite2 标准库类型
  - 城市精度提升（例如 `182.91.80.200` 可解析到广西桂林，旧库仅有国家级与模糊坐标）
- **防火墙拦截日志去重**：同一来源 IP 在 10 分钟内只记录一次告警，避免被扫描时日志刷屏

### v1.3.5

- **WEB 面板 HTTPS 可在系统设置中配置**：「系统设置 → WEB 面板 HTTPS（SSL）」开启开关并直接粘贴证书/私钥 PEM 内容，保存后同端口热重启立即生效（无需重启进程），配置持久化到 `web_tls.json`
  - 证书无法加载时自动退回 HTTP 并告警，避免面板失联
  - `--tls-cert` / `--tls-key` 启动参数保留，并写入同一份持久化配置
- **新增 certd 证书自动部署 API**（独立 Token 鉴权，请求头 `X-API-Token`）
  - `GET /api/cert/targets` 列出可部署目标（WEB 面板与所有 HTTPS 监听器）
  - `GET /api/cert/read` 读取 WEB 面板或指定 HTTPS 监听器的证书与私钥
  - `POST /api/cert/deploy` 部署证书内容：`target=web` 写面板证书并热重启监听；`target=tunnel` 写入监听器证书、写回配置并重启对应隧道
  - 部署 Token 在设置页可查看/复制/重置，重置后旧 Token 立即失效
- **操作日志时间只显示到秒**（`YYYY-MM-DD HH:mm:ss`，列表与导出统一）
- **提供 macOS 桌面版预编译包**：`sshtunnel-darwin-amd64-desktop`、`sshtunnel-darwin-arm64-desktop`（GitHub Actions macOS runner 原生构建）

### v1.3.4

- **城市规则改为下拉选取**：配置页通过三级联动下拉（国家 → 省/州 → 城市）生成规则，选「全部（*）」表示通配
  - 下拉数据由城市级数据库（Merged-IP）自动生成，扫描结果缓存到 `geoip-cities.json`（首次约需数十秒，之后秒开）
  - 新增 `GET /api/geoip/cities?country=CN` 与桌面端 `App.CityCatalog`
  - 未启用城市库时仍可手动输入规则
- **隧道开机启动**：配置新增 `auto_start`，程序/服务启动时自动拉起已勾选的隧道（配合 systemd 服务即开机自启）
  - 配置页新增「开机启动」开关，仪表盘卡片显示「开机启动」标签

### v1.3.3

- **新增：代理监听端口简易防火墙**（配置页每个监听器可独立启用）
  - 黑名单（默认）/ 白名单两种模式
  - 自定义 IP 与 IP 段，完整支持 IPv4 与 IPv6（含 CIDR）
  - GeoIP 国家规则（ISO 国家代码），**内置 GeoLite2-Country 数据库，无需额外配置**；可用 `--geoip-db` 覆盖为外部数据库或传 `none` 禁用
  - **城市级 GeoIP 规则**：`国家/省/城市` 格式（如 `CN/GD/Shenzhen`），各段支持 `*` 通配；城市库在「系统设置」中按需下载并启用（GeoLite2-City，约 49MB），即时热加载，关闭即回退内置国家库
  - IP 规则、国家规则与城市规则取并集；白名单模式下未知来源默认拒绝
  - 连接被拒时记录告警日志（含来源 IP）

### v1.3.2

- **修复**：升级后点击侧边栏「操作日志」等菜单无任何反应
  - 根因：服务端未设置缓存头，浏览器复用旧 `index.html`，其引用的旧 hash 资源在升级后已不存在；而缺失资源又被 SPA fallback 返回成 HTML，导致动态导入失败、路由导航被中止
  - 入口 `index.html` / 前端路由返回 `Cache-Control: no-cache`，带 hash 的 `assets/` 使用 `immutable` 长缓存
  - 缺失的静态资源（带扩展名）返回 404，不再错误回退为 HTML
  - 前端新增 `router.onError` 兜底：懒加载失败时自动整页刷新一次以获取新资源

### v1.3.1

- **修复**：开启 WEB 访问密码后，页面在登录页与主界面之间来回跳转
  - JWT 密钥持久化到 `settings.json`，服务重启/升级后已签发的 Token 仍然有效（不再被踢回登录页）
  - 401 统一走 hash 路由跳转（`#/login`），不再整页刷新到 `/login` 后被二次重定向
  - 401 处理幂等：并发/迟到的 401 不再清除刚登录获得的新 Token
  - 路由守卫每次导航读取最新鉴权状态，避免设置页开启/关闭密码后与后端不一致
  - 主界面数据改为进入即加载（登录后跳回主界面不再空白）

### v1.3.0

- **代理监听端口连通性测试**：配置页每个代理监听器新增「测试代理端口」按钮，经该监听端口访问 `https://api.ip.sb/ip` 并显示出口 IP 与耗时，用于验证代理监听是否真正走通隧道
  - 支持 HTTP / HTTPS / SOCKS4 / SOCKS5 四种协议（含认证）
  - HTTPS 代理的自签证书会被接受，隧道内目标证书仍严格校验
  - 新增 `POST /api/proxy/test`（WEB）与 `App.TestProxyListener`（桌面）
  - 安全约束：仅探测本机回环端口，防 SSRF
- 修复 `TestHTTPProxy_CONNECT` 既有偶发失败（bufio 缓冲导致的回显错位）

### v1.2.0

- **WEB 访问密码可在「系统设置」中动态开启/关闭**（默认不开启）：新增 `GET/PUT /api/auth/settings`，凭据以 bcrypt 哈希持久化到 `settings.json`（0600），开启即时生效无需重启，开启成功自动为当前会话签发 token；`--auth` 启动参数保留为临时覆盖
- 安装脚本：默认以 WEB 模式安装 systemd 服务（`--no-service` 跳过），默认监听 `0.0.0.0:8080`；修复 systemd 无 `HOME` 导致的启动失败（unit 内置 XDG 环境）；`-v` 版本号自动补 `v` 前缀；32 位 ARM 明确报错

### v1.1.0

**项目重命名与桌面版变体**

- 项目统一命名为 **SSHTunnel**（Go 模块 `sshsuidao` → `sshtunnel`）
- 二进制资产、安装脚本、更新检测、User-Agent、Wails 标题、前端 TOKEN_KEY 全部统一
- 新增 **桌面版变体**（`buildVariant=desktop`），通过 `-ldflags` 注入，资产名带 `-desktop` 后缀
- `updater` 包 `AssetName`/`AssetForPlatform` 支持 desktop 变体
- `/api/version` 与 `/api/version/check` 返回 `variant` 字段
- `--check-update` 与安装脚本按当前变体匹配对应资产

**新增 CLI 参数**

- `--version`：打印版本信息并退出
- `--check-update`：检查 GitHub Release 是否有新版本（退出码 0=最新 / 1=有更新 / 2=检查失败）
- `--repo`：指定 GitHub 仓库（默认 `xuanlove/SSHTunnel`）

**资产清单**

| 文件 | 平台 | 变体 |
|------|------|------|
| `sshtunnel-linux-amd64` | Linux x86_64 | web |
| `sshtunnel-linux-arm64` | Linux arm64 | web |
| `sshtunnel-darwin-amd64` | macOS Intel | web |
| `sshtunnel-darwin-arm64` | macOS Apple Silicon | web |
| `sshtunnel-windows-amd64.exe` | Windows x86_64 | web（控制台） |
| `sshtunnel-windows-amd64-desktop.exe` | Windows x86_64 | desktop（GUI） |

> macOS 桌面版需 osxcross 交叉编译或在 macOS 原生构建，本版本未提供预编译包。

### v1.0.0

**初始发布**

- 多跳跳板链、多协议代理（HTTP/HTTPS/SOCKS4/SOCKS5）、本地端口转发
- 自动重连（指数退避）、端口冲突检测
- 桌面端（Wails）与 WEB 面板双形态管理界面
- JWT 鉴权、TLS 支持、WebSocket 实时推送
- Linux systemd 安装脚本、GitHub Release 版本检测
- 全平台交叉编译（Linux/macOS/Windows × amd64/arm64）

---

## 第三方数据与许可

本程序内置 **MaxMind GeoLite2 Country** 数据库（`internal/proxy/geodata/GeoLite2-Country.mmdb`），用于代理防火墙的国家判定；城市级数据库（Merged-IP）不随程序分发，由用户在「系统设置」中按需下载。

- 数据来源：[MaxMind GeoLite2](https://www.maxmind.com/en/geoip2-databases)（内置国家库）、[NetworkCats Merged-IP-Data](https://github.com/NetworkCats/Merged-IP-Data)（城市级，聚合 MaxMind/ipinfo 等公开数据）
- 许可协议：GeoLite2 数据遵循 **CC BY-SA 4.0**；Merged-IP 数据请遵循其仓库声明
- 城市库下载后保存于用户配置目录（`sshtunnel/Merged-IP.mmdb`）
- 署名要求已在本节及 [geodata/README.md](internal/proxy/geodata/README.md) 中声明
- 如需更新内置数据，替换该 `.mmdb` 文件后重新编译即可

> GeoIP 判定结果仅供参考。私有/保留地址与部分 Anycast IP（如 `1.1.1.1`）可能无归属记录，此时按「未命中」处理（白名单拒绝、黑名单放行）。

---

## 技术栈

| 层级 | 技术 | 版本 |
|------|------|------|
| 后端语言 | Go | 1.27.4+ |
| SSH 协议 | `golang.org/x/crypto/ssh` | v0.53.0 |
| 桌面框架 | Wails | v2.12.0 |
| 前端框架 | Vue 3 | 3.5 |
| UI 组件库 | Element Plus | 2.14 |
| 状态管理 | Pinia | 2.3 |
| 路由 | Vue Router | 4.6 |
| 构建工具 | Vite | 6 |
| 类型检查 | TypeScript + vue-tsc | 5.7 / 2.2 |
| 实时通信 | `github.com/coder/websocket` | v1.8.15 |
| 鉴权 | `github.com/golang-jwt/jwt/v5` | v5.3.1 |
| 唯一标识 | `github.com/google/uuid` | v1.6.0 |
| GeoIP | `github.com/oschwald/maxminddb-golang` + 内置 GeoLite2-Country / 可选 Merged-IP 城市库 | v1.10.0 |

---

## 许可证

Copyright © 2026 SSHTunnel. All rights reserved.

详见 [LICENSE](LICENSE)。
