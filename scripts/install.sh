#!/usr/bin/env bash
#
# sshtunnel Linux 安装脚本
#
# 功能：
#   1. 自动检测系统架构（amd64 / arm64）
#   2. 从 GitHub Release 获取最新版本号
#   3. 下载对应平台的二进制资产
#   4. 安装到 /usr/local/bin（需要 root 或 sudo）
#   5. 默认安装为 systemd 服务，以 WEB 模式运行（可用 --no-service 跳过）
#   6. 安装后调用 --version 与 --check-update 验证
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/xuanlove/SSHTunnel/main/scripts/install.sh | sudo bash
#   或：
#   sudo bash scripts/install.sh [选项]
#
# 选项：
#   -r, --repo owner/repo   GitHub 仓库（默认 xuanlove/SSHTunnel）
#   -v, --version VERSION   安装指定版本而非最新（如 v1.0.0）
#   -d, --dir PATH          安装目录（默认 /usr/local/bin）
#   -s, --service           安装为 systemd 服务（默认启用，保留此选项兼容旧用法）
#   -n, --no-service        仅安装二进制，不安装 systemd 服务
#   -u, --upgrade           升级模式：保留现有配置，仅替换二进制
#   -h, --help              显示帮助
#
# 服务参数（非交互模式 / 管道安装时生效，交互终端下会询问）：
#   WEB_HOST   监听地址（默认 0.0.0.0）
#   WEB_PORT   监听端口（默认 8080）
#   WEB_AUTH   访问密码 user:password（默认无密码）
#
set -euo pipefail

# ==================== 默认配置 ====================
REPO="xuanlove/SSHTunnel"
INSTALL_DIR="/usr/local/bin"
BINARY_NAME="sshtunnel"
INSTALL_SERVICE=true   # 默认安装为 systemd 服务（WEB 模式）
UPGRADE_MODE=false
TARGET_VERSION=""

# ==================== 颜色输出 ====================
if [[ -t 1 ]]; then
    RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[0;33m'
    BLUE=$'\033[0;34m'; BOLD=$'\033[1m'; NC=$'\033[0m'
else
    RED=""; GREEN=""; YELLOW=""; BLUE=""; BOLD=""; NC=""
fi

# 注意：所有日志统一输出到 stderr，保证 stdout 仅用于函数返回值
# （download_binary 通过 stdout 返回临时文件路径，被 $(...) 捕获）
info()  { echo "${GREEN}[INFO]${NC} $*" >&2; }
warn()  { echo "${YELLOW}[WARN]${NC} $*" >&2; }
error() { echo "${RED}[ERROR]${NC} $*" >&2; }
step()  { echo "${BLUE}==>${NC} ${BOLD}$*${NC}" >&2; }

# ==================== 帮助 ====================
show_help() {
    cat <<EOF
${BOLD}sshtunnel Linux 安装脚本${NC}

从 GitHub Release 下载并安装 sshtunnel 二进制。

${BOLD}用法:${NC}
  sudo bash install.sh [选项]

${BOLD}选项:${NC}
  -r, --repo owner/repo   GitHub 仓库（默认 ${REPO}）
  -v, --version VERSION    安装指定版本而非最新（如 v1.0.0）
  -d, --dir PATH           安装目录（默认 ${INSTALL_DIR}）
  -s, --service            安装为 systemd 服务（默认启用，保留兼容）
  -n, --no-service         仅安装二进制，不安装 systemd 服务
  -u, --upgrade            升级模式：保留现有配置，仅替换二进制
  -h, --help               显示此帮助

${BOLD}服务参数${NC}（非交互模式生效，交互终端下会询问）:
  WEB_HOST   监听地址（默认 0.0.0.0）
  WEB_PORT   监听端口（默认 8080）
  WEB_AUTH   访问密码 user:password（默认无密码）

${BOLD}示例:${NC}
  # 一键安装最新版（默认安装为 WEB 模式的 systemd 服务）
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh | sudo bash

  # 安装指定版本
  sudo bash install.sh -v v1.0.0

  # 仅安装二进制，不注册服务
  sudo bash install.sh --no-service

  # 非交互配置服务（管道安装场景）
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh | sudo WEB_PORT=8090 WEB_AUTH=admin:pass bash

  # 升级（保留配置）
  sudo bash install.sh -u
EOF
    exit 0
}

# ==================== 参数解析 ====================
while [[ $# -gt 0 ]]; do
    case "$1" in
        -r|--repo)     REPO="$2"; shift 2 ;;
        -v|--version)  TARGET_VERSION="$2"; shift 2 ;;
        -d|--dir)      INSTALL_DIR="$2"; shift 2 ;;
        -s|--service)  INSTALL_SERVICE=true; shift ;;
        -n|--no-service) INSTALL_SERVICE=false; shift ;;
        -u|--upgrade)  UPGRADE_MODE=true; shift ;;
        -h|--help)     show_help ;;
        *) error "未知参数: $1"; exit 1 ;;
    esac
done

# ==================== 前置检查 ====================
check_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        error "缺少依赖命令: $1（请先安装）"
        exit 1
    fi
}

check_command curl
check_command uname

# root 检查（安装到系统目录需要 root）
if [[ "$INSTALL_DIR" == /usr/* || "$INSTALL_DIR" == /opt/* ]] && [[ $EUID -ne 0 ]]; then
    error "安装到系统目录 ${INSTALL_DIR} 需要 root 权限，请使用 sudo 运行"
    exit 1
fi

# 服务安装需要 root 与 systemd
if $INSTALL_SERVICE; then
    if [[ $EUID -ne 0 ]]; then
        error "默认安装为 systemd 服务，需要 root 权限：请使用 sudo 运行，或加 --no-service 仅安装二进制"
        exit 1
    fi
    if ! command -v systemctl >/dev/null 2>&1; then
        warn "未检测到 systemctl（非 systemd 系统），跳过服务安装，仅安装二进制"
        INSTALL_SERVICE=false
    fi
fi

# ==================== 架构检测 ====================
detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64)  arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        armv6l|armv7l|armv8l)
            # 32 位 ARM 用户态（含 64 位内核跑 32 位系统的情况），无法运行 arm64 二进制
            error "检测到 32 位 ARM 架构（${arch}），当前未提供对应的预编译二进制"
            error "解决方案："
            error "  1) 使用 64 位系统（uname -m 显示 aarch64 后重试本脚本）"
            error "  2) 从源码构建 32 位二进制："
            error "     GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -o sshtunnel ."
            exit 1
            ;;
        *)
            error "不支持的架构: $arch（当前仅提供 amd64/arm64 预编译二进制）"
            error "可从源码构建: GOOS=linux GOARCH=<目标架构> CGO_ENABLED=0 go build -o sshtunnel ."
            exit 1
            ;;
    esac
    if [[ "$os" != "linux" ]]; then
        warn "检测到非 Linux 系统: $os，此脚本专为 Linux 设计，继续安装..."
    fi
    PLATFORM="${os}-${arch}"
    ASSET_NAME="${BINARY_NAME}-${PLATFORM}"
}

# ==================== 获取最新版本 ====================
# 利用 GitHub /releases/latest 的 302 重定向解析最新 tag，无需 jq。
# 相比 api.github.com 不受未认证请求限流影响。
get_latest_version() {
    local url="https://github.com/${REPO}/releases/latest"
    local final_url
    # -I 仅取头，-o 输出到文件，-w 输出最终 URL
    final_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$url" 2>/dev/null || true)"
    if [[ -z "$final_url" ]]; then
        error "无法获取最新版本（网络问题或仓库 ${REPO} 无 Release）"
        exit 1
    fi
    # 最终 URL 形如 https://github.com/owner/repo/releases/tag/v1.0.0
    local tag="${final_url##*/tag/}"
    if [[ -z "$tag" || "$tag" == "$final_url" ]]; then
        error "解析版本号失败: ${final_url}"
        error "可能原因：仓库 ${REPO} 不存在，或尚无任何 Release"
        exit 1
    fi
    echo "$tag"
}

# 规范化版本标签：GitHub Release 标签约定为 vX.Y.Z。
# 用户通过 -v 输入 1.1.0（缺 v 前缀）时自动补 v，避免资产 URL 404。
normalize_version() {
    local v="$1"
    if [[ "$v" != v* && "$v" =~ ^[0-9] ]]; then
        v="v${v}"
    fi
    echo "$v"
}

# ==================== 下载二进制 ====================
download_binary() {
    local version="$1"
    local download_url="https://github.com/${REPO}/releases/download/${version}/${ASSET_NAME}"
    # 使用私有临时目录，避免可预测路径被本地用户预建符号链接利用
    local tmp_dir
    tmp_dir="$(mktemp -d)"
    local tmp_file="${tmp_dir}/${ASSET_NAME}"

    step "下载 ${ASSET_NAME} (版本 ${version})"
    info "URL: ${download_url}"

    if ! curl -fSL --progress-bar -o "$tmp_file" "$download_url"; then
        error "下载失败。可能该版本未提供 ${PLATFORM} 二进制。"
        error "请确认标签存在（如 v1.1.0），或访问 https://github.com/${REPO}/releases 查看可用资产"
        rm -rf "$tmp_dir"
        exit 1
    fi

    # 校验为可执行文件（非 HTML 错误页）
    local file_type
    file_type="$(file -b "$tmp_file" 2>/dev/null || echo "unknown")"
    if echo "$file_type" | grep -qi "HTML"; then
        error "下载内容是 HTML 而非二进制，可能版本号或资产名错误"
        head -c 200 "$tmp_file" >&2
        rm -rf "$tmp_dir"
        exit 1
    fi

    echo "$tmp_file"
}

# ==================== 安装二进制 ====================
install_binary() {
    local tmp_file="$1"
    local target="${INSTALL_DIR}/${BINARY_NAME}"
    local old_version=""

    # 升级模式：记录旧版本
    if [[ -x "$target" ]]; then
        old_version="$("$target" --version 2>/dev/null | head -1 | awk '{print $2}')" || old_version=""
    fi

    step "安装到 ${target}"
    install -m 0755 "$tmp_file" "$target"
    rm -f "$tmp_file"
    rmdir "$(dirname "$tmp_file")" 2>/dev/null || true

    if [[ -n "$old_version" ]]; then
        info "已升级: ${old_version} -> ${INSTALLED_VERSION}"
    else
        info "安装完成: ${INSTALLED_VERSION}"
    fi
}

# ==================== 验证安装 ====================
verify_install() {
    local target="${INSTALL_DIR}/${BINARY_NAME}"
    step "验证安装"

    if ! "$target" --version; then
        error "二进制无法执行，可能架构不匹配"
        exit 1
    fi

    info "检查更新..."
    "$target" --check-update || true
}

# ==================== systemd 服务安装 ====================
# 以 WEB 模式安装为 systemd 服务。
# 交互终端下询问监听地址/端口/密码；非交互（管道安装）时使用默认值或环境变量：
#   WEB_HOST（默认 127.0.0.1）、WEB_PORT（默认 8080）、WEB_AUTH（user:password，留空则生成随机密码）
install_service() {
    local target="${INSTALL_DIR}/${BINARY_NAME}"
    local svc_path="/etc/systemd/system/${BINARY_NAME}.service"
    local web_host="${WEB_HOST:-127.0.0.1}"
    local web_port="${WEB_PORT:-8080}"
    local auth="${WEB_AUTH:-}"
    local generated_pass=""
    if [[ -z "$auth" ]]; then
        # 未提供凭据时生成随机强密码，避免面板无密码暴露
        generated_pass="$(LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 24)"
        auth="admin:${generated_pass}"
    fi
    local start_now="Y"

    # 仅交互终端下询问；管道场景避免 read 吞掉脚本内容
    if [[ -t 0 ]]; then
        step "配置 systemd 服务"
        info "请回答以下问题（直接回车使用默认值）："
        local input
        read -rp "WEB 监听地址 [${web_host}]: " input; web_host="${input:-$web_host}"
        read -rp "WEB 监听端口 [${web_port}]: " input; web_port="${input:-$web_port}"
        read -rp "访问密码（user:password，留空无密码）: " input; auth="${input:-$auth}"
        read -rp "立即启动服务？[Y/n]: " input; start_now="${input:-Y}"
    fi

    # 端口合法性校验（环境变量可能是非法值）
    if ! [[ "$web_port" =~ ^[0-9]+$ ]] || (( web_port < 1 || web_port > 65535 )); then
        error "无效的 WEB 端口: ${web_port}（应为 1-65535）"
        exit 1
    fi

    # 安全提示：监听所有网卡时说明已自动生成密码
    if [[ "$web_host" == "0.0.0.0" || "$web_host" == "::" ]] && [[ -n "$generated_pass" ]]; then
        warn "服务将监听所有网卡，已自动生成随机访问密码（安装完成后可按提示查看）"
    fi

    # 凭据写入独立环境文件（0600），避免出现在 systemd unit 中
    local env_dir="/etc/sshtunnel"
    local env_file="${env_dir}/sshtunnel.env"
    mkdir -p "$env_dir"
    chmod 0700 "$env_dir"
    ( umask 077; printf 'SSHTUNNEL_AUTH=%s\n' "$auth" > "$env_file" )
    chmod 0600 "$env_file"

    # 专用系统用户（无法登录）
    local run_as="sshtunnel"
    if ! id -u "$run_as" >/dev/null 2>&1; then
        if ! useradd --system --home-dir "/var/lib/${BINARY_NAME}" --shell /usr/sbin/nologin "$run_as" 2>/dev/null; then
            warn "无法创建 ${run_as} 用户，服务将以 root 运行"
            run_as=""
        fi
    fi

    local exec_args="--mode=web --web-host=${web_host} --web-port=${web_port}"

    step "安装 systemd 服务"
    cat > "$svc_path" <<EOF
[Unit]
Description=SSHTunnel (WEB mode)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
${run_as:+User=${run_as}}
${run_as:+Group=${run_as}}
EnvironmentFile=${env_file}
# systemd 服务不继承 HOME，而程序依赖 os.UserConfigDir/UserCacheDir 定位配置与日志，
# 因此显式设置 HOME 与 XDG 目录；StateDirectory 负责创建 /var/lib/sshtunnel
StateDirectory=sshtunnel
Environment=HOME=/var/lib/sshtunnel
Environment=XDG_CONFIG_HOME=/var/lib/sshtunnel/config
Environment=XDG_CACHE_HOME=/var/lib/sshtunnel/cache
Environment=XDG_DATA_HOME=/var/lib/sshtunnel/data
ExecStart=${target} ${exec_args} --auth=\${SSHTUNNEL_AUTH}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
    chmod 0600 "$svc_path"

    systemctl daemon-reload
    systemctl enable "${BINARY_NAME}.service" >/dev/null 2>&1
    info "服务已安装并设为开机自启: ${svc_path}"

    if [[ "$start_now" =~ ^[Yy]$ ]]; then
        # restart：未运行则启动，已运行（升级场景）则应用新二进制
        systemctl restart "${BINARY_NAME}"
        sleep 1
        if systemctl is-active --quiet "${BINARY_NAME}"; then
            local addr="${web_host}"
            [[ "$addr" == "0.0.0.0" || "$addr" == "::" ]] && addr="<本机IP>"
            info "服务已启动，WEB 面板: http://${addr}:${web_port}"
            if [[ -n "$generated_pass" ]]; then
                info "已生成随机访问密码并写入 ${env_file}（0600）"
                info "查看密码: sudo cat ${env_file}"
            fi
        else
            warn "服务启动失败，请查看日志: journalctl -u ${BINARY_NAME} -e"
        fi
    else
        info "稍后启动: sudo systemctl start ${BINARY_NAME}"
    fi

    info "常用命令: systemctl status|restart|stop ${BINARY_NAME}"
    info "实时日志: journalctl -u ${BINARY_NAME} -f"
}

# ==================== 主流程 ====================
main() {
    echo "${BOLD}================ sshtunnel 安装程序 ================${NC}"

    step "检测系统架构"
    detect_platform
    info "平台: ${PLATFORM}"
    info "资产名: ${ASSET_NAME}"

    # 确定版本
    if [[ -n "$TARGET_VERSION" ]]; then
        local normalized
        normalized="$(normalize_version "$TARGET_VERSION")"
        if [[ "$normalized" != "$TARGET_VERSION" ]]; then
            info "版本号已规范化: ${TARGET_VERSION} -> ${normalized}"
        fi
        INSTALLED_VERSION="$normalized"
        info "使用指定版本: ${INSTALLED_VERSION}"
    else
        step "获取最新版本"
        INSTALLED_VERSION="$(get_latest_version)"
        info "最新版本: ${INSTALLED_VERSION}"
    fi

    # 升级模式提示
    if $UPGRADE_MODE && [[ -x "${INSTALL_DIR}/${BINARY_NAME}" ]]; then
        local current
        current="$("${INSTALL_DIR}/${BINARY_NAME}" --version 2>/dev/null | head -1 | awk '{print $2}')" || current="未知"
        info "升级模式：当前已安装 ${current}"
    fi

    # 下载
    local tmp_file
    tmp_file="$(download_binary "$INSTALLED_VERSION")"

    # 安装
    install_binary "$tmp_file"

    # 验证
    verify_install

    # 默认安装 systemd 服务（WEB 模式）；--no-service 跳过
    if $INSTALL_SERVICE; then
        install_service
    fi

    echo ""
    echo "${GREEN}${BOLD}==== 安装成功 ====${NC}"
    info "版本: ${INSTALLED_VERSION}"
    info "位置: ${INSTALL_DIR}/${BINARY_NAME}"
    info "查看版本: ${BINARY_NAME} --version"
    info "检查更新: ${BINARY_NAME} --check-update"
    if ! $INSTALL_SERVICE; then
        info "启动 WEB 面板: ${BINARY_NAME} --mode=web --web-port=8080"
    fi
}

main "$@"
