# SSHTunnel

> 一款跨平台的 SSH 隧道管理工具，基于 Wails v2 + Go 1.25 + Vue 3 构建。
> 支持多跳跳板链、多协议代理（HTTP/HTTPS/SOCKS4/SOCKS5）、本地端口转发、自动重连、实时日志推送，并提供**桌面端**与 **WEB 面板**双形态管理界面。

[![Go Version](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://go.dev)
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
- **认证方式**：密码认证 / 密钥认证（密钥以 PEM 文本形式存储，便于跨设备同步）
- **自动重连**：SSH 断开后指数退避重连（初始 2s，上限 60s，可配置无限重试）
- **端口冲突检测**：监听启动前自动检测端口占用，避免冲突

### 代理协议细节

| 协议 | 认证 | 说明 |
|------|------|------|
| HTTP | Basic Auth（可选） | 支持 `CONNECT` 方法代理 HTTPS 流量 |
| HTTPS | Basic Auth（可选） | 需提供 TLS 证书（可一键生成自签证书） |
| SOCKS4 | UserID（可选） | 兼容 SOCKS4a（域名直连） |
| SOCKS5 | 用户名/密码（可选） | 实现 RFC 1928 / RFC 1929 |

### 管理界面

- **桌面端**：Wails + WebView 原生应用（Windows / Linux / macOS）
- **WEB 面板**：内置 HTTP/HTTPS 服务器，浏览器即可管理（可选密码访问）
- **实时推送**：日志与隧道状态变更通过 WebSocket 实时推送到前端
- **JWT 鉴权**：WEB 模式下可选启用密码访问，Token 有效期 24 小时
- **TLS 支持**：WEB 面板与 HTTPS 代理均可使用 TLS

---

## 快速开始

### 一键安装（Linux）

```bash
curl -fsSL https://raw.githubusercontent.com/xuanlove/SSHTunnel/main/scripts/install.sh | sudo bash
```

安装完成后：

```bash
sshtunnel --version          # 查看版本
sshtunnel --mode=web --web-port=8090 --auth=admin:yourpassword
```

浏览器访问 `http://<服务器IP>:8090` 进入管理面板。

### 下载预编译二进制

前往 [Releases 页面](https://github.com/xuanlove/SSHTunnel/releases/latest) 下载对应平台的二进制：

| 文件 | 平台 | 变体 |
|------|------|------|
| `sshtunnel-linux-amd64` | Linux x86_64 | web |
| `sshtunnel-linux-arm64` | Linux arm64 | web |
| `sshtunnel-darwin-amd64` | macOS Intel | web |
| `sshtunnel-darwin-arm64` | macOS Apple Silicon | web |
| `sshtunnel-windows-amd64.exe` | Windows x86_64 | web（控制台） |
| `sshtunnel-windows-amd64-desktop.exe` | Windows x86_64 | desktop（GUI） |

> macOS 桌面版（desktop 变体）需在 macOS 上原生构建或借助 osxcross 交叉编译，Release 暂不提供预编译包。

---

## 安装

### 方式一：安装脚本（推荐）

```bash
# 安装最新版
curl -fsSL https://raw.githubusercontent.com/xuanlove/SSHTunnel/main/scripts/install.sh | sudo bash

# 安装指定版本
sudo bash scripts/install.sh -v v1.1.0

# 升级模式（保留现有配置，仅替换二进制）
sudo bash scripts/install.sh -u

# 安装并注册为 systemd 服务（交互式配置端口/密码）
sudo bash scripts/install.sh -s
```

脚本选项：

| 选项 | 说明 |
|------|------|
| `-r, --repo owner/repo` | GitHub 仓库（默认 `xuanlove/SSHTunnel`） |
| `-v, --version VERSION` | 安装指定版本而非最新（如 `v1.1.0`） |
| `-d, --dir PATH` | 安装目录（默认 `/usr/local/bin`） |
| `-s, --service` | 安装为 systemd 服务（交互式配置端口/密码） |
| `-u, --upgrade` | 升级模式：保留配置，仅替换二进制 |
| `-h, --help` | 显示帮助 |

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

- Go 1.25+
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
# 监听所有网卡
./sshtunnel --mode=web --web-host=0.0.0.0 --web-port=8090
```

浏览器访问 `http://127.0.0.1:8090` 进入管理面板。

### WEB 模式（密码保护）

```bash
./sshtunnel --mode=web --web-port=8090 --auth=admin:yourpassword
```

首次访问跳转至登录页，输入凭据后获取 JWT Token（24 小时有效）。

### 启用 HTTPS（WEB 面板）

```bash
./sshtunnel --mode=web --web-port=8443 \
  --tls-cert=/path/to/cert.pem \
  --tls-key=/path/to/key.pem \
  --auth=admin:password
```

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
| `--auth` | 空 | 启用密码访问，格式 `user:password`，留空则无密码 |
| `--tls-cert` | 空 | TLS 证书路径（启用 HTTPS） |
| `--tls-key` | 空 | TLS 私钥路径（启用 HTTPS） |
| `--version` | - | 打印版本信息并退出 |
| `--check-update` | - | 检查 GitHub Release 是否有新版本并退出 |
| `--repo` | `xuanlove/SSHTunnel` | GitHub 仓库（owner/repo），用于版本检查 |

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
        "tls": {                      // 仅 https 协议
          "cert_file": "/path/to/cert.pem",
          "key_file": "/path/to/key.pem"
        }
      }
    ],
    "auto_reconnect": true,          // 是否自动重连
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
| `status` | string | 运行时状态，不持久化（`stopped` / `starting` / `running` / `error`） |

### 代理监听器

单条隧道可配置多个监听器，每个监听器独立指定：

- 协议（`http` / `https` / `socks4` / `socks5`）
- 监听端口
- 是否允许外部访问（`0.0.0.0` 或 `127.0.0.1`）
- 认证配置（HTTP Basic / SOCKS5 用户名密码 / SOCKS4 UserID）
- TLS 证书（仅 HTTPS，可在设置页一键生成自签证书）

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
| GET | `/api/logs?limit=N` | 获取最近 N 条日志 |
| POST | `/api/port/check` | 检测端口可用性 |
| POST | `/api/cert/generate` | 生成 HTTPS 自签证书 |
| POST | `/api/hopchain/parse` | 解析跳板链简写 |
| POST | `/api/tunnel/test` | 测试隧道连通性（不启动监听） |
| GET | `/api/version` | 查询当前版本信息 |
| GET | `/api/version/check` | 检查 GitHub Release 新版本 |

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
  - `server.go` — HTTP 服务器、路由树、SPA fallback、`writeJSON/writeError`
  - `handlers.go` — API 路由（configs / tunnels / logs / version 等）
  - `auth.go` — `authMiddleware`、JWT 签发/校验（HS256，24h 过期）
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
| `Settings.vue` | 系统设置（端口检测、自签证书生成、版本信息） |

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
# 全部测试
go test ./...

# 带竞态检测
go test -race ./...

# 指定包
go test ./internal/tunnel/...
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

# macOS 桌面版（需在 macOS 原生或 osxcross 交叉编译）
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
  go build -ldflags "${LDFLAGS_DESKTOP}" -o releases/sshtunnel-darwin-arm64-desktop .
```

> `-H=windowsgui` 用于 Windows 桌面版，隐藏控制台窗口（GUI 子系统）。

### 资产命名规则

| 变体 | 命名规则 | 示例 |
|------|----------|------|
| web（默认） | `sshtunnel-{os}-{arch}[.exe]` | `sshtunnel-linux-amd64` |
| desktop | `sshtunnel-{os}-{arch}-desktop[.exe]` | `sshtunnel-windows-amd64-desktop.exe` |

`--check-update` 与安装脚本会按当前二进制的 `buildVariant` 匹配对应资产。

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

1. **WEB 面板监听公网时务必启用 `--auth`**：无密码模式监听 `0.0.0.0` 会让任何能访问本机端口的人控制隧道，启动时会在日志中输出安全警告
2. **生产环境推荐启用 HTTPS**：使用 `--tls-cert` / `--tls-key` 防止 Token 被中间人窃取
3. **SSH 密钥以 PEM 文本存储于配置文件**：请妥善保护 `configs.json`，避免提交到版本控制系统
4. **JWT 密钥每次启动随机生成**：重启后所有已签发的 Token 失效，需重新登录
5. **systemd 部署已内置安全加固**：`ProtectSystem=strict`、`PrivateTmp=true`、`NoNewPrivileges=true`、专用 `nologin` 系统用户

---

## 版本更新说明

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

## 技术栈

| 层级 | 技术 | 版本 |
|------|------|------|
| 后端语言 | Go | 1.25+ |
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

---

## 许可证

Copyright © 2026 SSHTunnel. All rights reserved.

详见 [LICENSE](LICENSE)。
