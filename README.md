# SSHTunnel

一款跨平台的 SSH 隧道管理工具，基于 Wails v2 + Go + Vue 3 构建。支持多跳跳板链、多协议代理（HTTP/HTTPS/SOCKS4/SOCKS5）、本地端口转发、自动重连、实时日志推送，并提供桌面端与 WEB 面板双形态管理界面。

## 功能特性

### 隧道能力
- **多跳跳板链**：支持任意层级的 SSH 跳板，串联多条 SSH 连接
- **多协议代理**：HTTP / HTTPS / SOCKS4 / SOCKS5，单隧道可同时监听多端口多协议
- **本地端口转发**：经典的 SSH `-L` 本地转发
- **认证方式**：密码认证 / 密钥认证（密钥以 PEM 文本形式存储，便于跨设备同步）
- **自动重连**：SSH 连接断开后自动重连，采用指数退避（初始 2s，上限 60s）
- **端口冲突检测**：监听启动前自动检测端口占用

### 管理界面
- **桌面端**：基于 Wails + WebView 的原生应用（Windows / Linux / macOS）
- **WEB 面板**：内置 HTTP/HTTPS 服务器，浏览器即可管理（可选密码访问）
- **实时推送**：日志和隧道状态变更通过 WebSocket 实时推送到前端
- **JWT 鉴权**：WEB 模式下可选启用密码访问，Token 有效期 24 小时
- **TLS 支持**：WEB 面板与 HTTPS 代理均可使用 TLS

### 代理协议细节
| 协议 | 认证 | 说明 |
|------|------|------|
| HTTP | Basic Auth（可选） | 支持 `CONNECT` 方法代理 HTTPS 流量 |
| HTTPS | Basic Auth（可选） | 需提供 TLS 证书（可一键生成自签证书） |
| SOCKS4 | UserID（可选） | 兼容 SOCKS4a（域名直连） |
| SOCKS5 | 用户名/密码（可选） | 实现 RFC 1928 / RFC 1929 |

## 运行模式

通过 `--mode` 参数切换：

| 模式 | 说明 |
|------|------|
| `desktop` | 默认。仅启动桌面端应用 |
| `web` | 仅启动 WEB 面板（无 GUI 依赖，适合服务器部署） |
| `both` | 同时启动桌面端与 WEB 面板 |

## 安装与构建

### 依赖
- Go 1.25+
- Node.js 18+ 与 npm（构建前端）
- 桌面模式额外依赖：
  - Windows：无
  - Linux：`libwebkit2gtk-4.1-dev` `libgtk-3-dev`
  - macOS：Xcode Command Line Tools

### 从源码构建

```bash
# 安装 wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

# 克隆项目
git clone <repo-url>
cd sshtunnel

# 构建前端
cd frontend && npm install && npm run build && cd ..

# 构建当前平台桌面端（CGO 启用）
go build -o build/bin/sshtunnel .

# 仅构建 WEB 模式二进制（CGO 禁用，可交叉编译）
CGO_ENABLED=0 go build -o build/bin/sshtunnel-web .
```

### 交叉编译全平台

WEB 模式二进制可纯 Go 交叉编译，无平台依赖。版本号通过 `-ldflags` 注入：

```bash
VERSION=v1.1.0
COMMIT=$(git rev-parse --short HEAD)
LDFLAGS="-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X main.buildVariant=web"

# Linux amd64
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "${LDFLAGS}" -o build/bin/sshtunnel-linux-amd64 .

# Linux arm64
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "${LDFLAGS}" -o build/bin/sshtunnel-linux-arm64 .

# macOS amd64 (Intel)
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "${LDFLAGS}" -o build/bin/sshtunnel-darwin-amd64 .

# macOS arm64 (Apple Silicon)
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "${LDFLAGS}" -o build/bin/sshtunnel-darwin-arm64 .

# Windows amd64
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "${LDFLAGS}" -o build/bin/sshtunnel-windows-amd64.exe .
```

### 桌面版变体（含 WebView）

桌面版通过 `buildVariant=desktop` 区分，资产名带 `-desktop` 后缀。需启用 CGO：

```bash
LDFLAGS_DESKTOP="-X main.version=${VERSION} -X main.commit=${COMMIT} -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X main.buildVariant=desktop"

# Windows amd64 桌面版（Linux 宿主用 mingw-w64 交叉编译）
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
  go build -ldflags "${LDFLAGS_DESKTOP}" -o build/bin/sshtunnel-windows-amd64-desktop.exe .

# macOS 桌面版需在 macOS 上或借助 osxcross 交叉编译
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
  go build -ldflags "${LDFLAGS_DESKTOP}" -o build/bin/sshtunnel-darwin-arm64-desktop .
```

> Linux 桌面版需在 Linux 宿主上启用 CGO 编译；macOS 桌面版交叉编译需 osxcross 工具链。详见 Wails 官方文档。

### 资产命名规则

| 变体 | 命名规则 | 示例 |
|------|----------|------|
| web（默认） | `sshtunnel-{os}-{arch}[.exe]` | `sshtunnel-linux-amd64` |
| desktop | `sshtunnel-{os}-{arch}-desktop[.exe]` | `sshtunnel-windows-amd64-desktop.exe` |

`--check-update` 与安装脚本会按当前变体匹配对应资产。

## 使用方式

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

浏览器访问 `http://127.0.0.1:8090` 即可进入管理面板。

### WEB 模式（密码保护）

```bash
./sshtunnel --mode=web --web-port=8090 --auth=admin:yourpassword
```

首次访问将跳转至登录页，输入凭据后获取 JWT Token（24 小时有效）。

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

## 命令行参数

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

退出码：`0` 表示已是最新；`1` 表示有新版本；`2` 表示检查失败。

## 配置说明

### 隧道配置

配置文件存储位置：
- Windows：`%APPDATA%\sshtunnel\configs.json`
- Linux：`~/.config/sshtunnel/configs.json`
- macOS：`~/Library/Application Support/sshtunnel/configs.json`

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

### 代理监听器

单条隧道可配置多个监听器，每个监听器独立指定：
- 协议（http/https/socks4/socks5）
- 监听端口
- 是否允许外部访问（`0.0.0.0` 或 `127.0.0.1`）
- 认证配置（HTTP Basic / SOCKS5 用户名密码 / SOCKS4 UserID）
- TLS 证书（仅 HTTPS）

## WEB API

### 公开端点
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/auth/status` | 查询鉴权状态 |
| POST | `/api/login` | 登录（仅密码模式注册） |

### 受保护端点（密码模式需 `Authorization: Bearer <token>`）

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

### WebSocket 端点

密码模式下通过 `?token=<jwt>` 携带鉴权：

| 路径 | 说明 |
|------|------|
| `/api/logs/stream` | 日志实时流（`type: "log"`） |
| `/api/tunnels/status/stream` | 隧道状态实时流（`type: "status"`） |

### 统一响应格式

```json
{
  "code": 0,
  "message": "success",
  "data": { }
}
```

`code=0` 表示成功，非 0 为业务错误码；HTTP 状态码与业务码类别对应（4xx/5xx）。

## 项目结构

```
sshtunnel/
├── main.go                  # 入口与运行模式切换
├── app.go                   # Wails 桌面端绑定层
├── ssh_helper.go            # SSH 辅助函数
├── internal/
│   ├── config/              # 配置管理（持久化、CRUD）
│   ├── logger/              # 日志（环形缓冲 + 多订阅者 sink）
│   ├── port/                # 端口检测
│   ├── proxy/               # 代理协议实现（HTTP/HTTPS/SOCKS4/SOCKS5）
│   ├── sshclient/           # SSH 客户端与跳板链解析
│   ├── tunnel/              # 隧道管理（启停、自动重连、状态回调）
│   └── web/                 # WEB 服务器、API、WebSocket、JWT 鉴权
├── frontend/                # Vue 3 + Element Plus 前端
│   ├── src/
│   │   ├── api/             # 双模式 API 适配层（Wails Call / HTTP fetch）
│   │   ├── components/      # HopEditor、ProxyListenerEditor
│   │   ├── router/          # 路由守卫（WEB 模式登录拦截）
│   │   ├── stores/          # Pinia 状态管理
│   │   └── views/           # Dashboard / ConfigEdit / Logs / Login / Settings
│   └── dist/                # 构建产物（go:embed 嵌入）
└── build/
    └── bin/                 # 编译产物
```

## 安全建议

1. **WEB 面板监听公网时务必启用 `--auth`**：无密码模式监听 `0.0.0.0` 会让任何能访问本机端口的人控制隧道，启动时会在日志中输出安全警告。
2. **生产环境推荐启用 HTTPS**：使用 `--tls-cert` / `--tls-key` 防止 Token 被中间人窃取。
3. **SSH 密钥以 PEM 文本存储于配置文件**：请妥善保护 `configs.json`，避免提交到版本控制系统。
4. **JWT 密钥每次启动随机生成**：重启后所有已签发的 Token 失效，需重新登录。

## 技术栈

| 层级 | 技术 |
|------|------|
| 后端 | Go 1.25+、`golang.org/x/crypto/ssh` |
| 桌面框架 | Wails v2 |
| 前端 | Vue 3、Element Plus、Pinia、Vue Router |
| 实时通信 | `github.com/coder/websocket` |
| 鉴权 | `github.com/golang-jwt/jwt/v5` |
| 唯一标识 | `github.com/google/uuid` |

## 许可证

Copyright © 2026 SSH Tunnel Manager. All rights reserved.
