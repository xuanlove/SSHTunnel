#!/usr/bin/env bash
#
# sshsuidao Linux 安装脚本
#
# 功能：
#   1. 自动检测系统架构（amd64 / arm64）
#   2. 从 GitHub Release 获取最新版本号
#   3. 下载对应平台的二进制资产
#   4. 安装到 /usr/local/bin（需要 root 或 sudo）
#   5. 安装后调用 --version 与 --check-update 验证
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
#   -s, --service           安装为 systemd 服务（交互式配置端口/密码）
#   -u, --upgrade           升级模式：保留现有配置，仅替换二进制
#   -h, --help              显示帮助
#
set -euo pipefail

# ==================== 默认配置 ====================
REPO="xuanlove/SSHTunnel"
INSTALL_DIR="/usr/local/bin"
BINARY_NAME="sshsuidao"
INSTALL_SERVICE=false
UPGRADE_MODE=false
TARGET_VERSION=""

# ==================== 颜色输出 ====================
if [[ -t 1 ]]; then
    RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[0;33m'
    BLUE=$'\033[0;34m'; BOLD=$'\033[1m'; NC=$'\033[0m'
else
    RED=""; GREEN=""; YELLOW=""; BLUE=""; BOLD=""; NC=""
fi

info()  { echo "${GREEN}[INFO]${NC} $*"; }
warn()  { echo "${YELLOW}[WARN]${NC} $*"; }
error() { echo "${RED}[ERROR]${NC} $*" >&2; }
step()  { echo "${BLUE}==>${NC} ${BOLD}$*${NC}"; }

# ==================== 帮助 ====================
show_help() {
    cat <<EOF
${BOLD}sshsuidao Linux 安装脚本${NC}

从 GitHub Release 下载并安装 sshsuidao 二进制。

${BOLD}用法:${NC}
  sudo bash install.sh [选项]

${BOLD}选项:${NC}
  -r, --repo owner/repo   GitHub 仓库（默认 ${REPO}）
  -v, --version VERSION    安装指定版本而非最新（如 v1.0.0）
  -d, --dir PATH           安装目录（默认 ${INSTALL_DIR}）
  -s, --service            安装为 systemd 服务（交互式配置端口/密码）
  -u, --upgrade            升级模式：保留现有配置，仅替换二进制
  -h, --help               显示此帮助

${BOLD}示例:${NC}
  # 一键安装最新版
  curl -fsSL https://raw.githubusercontent.com/${REPO}/main/scripts/install.sh | sudo bash

  # 安装指定版本
  sudo bash install.sh -v v1.0.0

  # 升级（保留配置）
  sudo bash install.sh -u

  # 安装并注册为 systemd 服务
  sudo bash install.sh -s
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

# ==================== 架构检测 ====================
detect_platform() {
    local os arch
    os="$(uname -s | tr '[:upper:]' '[:lower:]')"
    arch="$(uname -m)"
    case "$arch" in
        x86_64|amd64)  arch="amd64" ;;
        aarch64|arm64) arch="arm64" ;;
        armv7l)        arch="arm64"; warn "armv7l 仅 arm64 二进制可尝试兼容，若无法运行请自行编译" ;;
        *)
            error "不支持的架构: $arch（当前仅提供 amd64/arm64 预编译二进制）"
            error "可从源码构建: go build ."
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
# 利用 GitHub /releases/latest 的 302 重定向解析最新 tag，无需 jq
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
        error "解析版本号失败: $final_url"
        exit 1
    fi
    echo "$tag"
}

# ==================== 下载二进制 ====================
download_binary() {
    local version="$1"
    local download_url="https://github.com/${REPO}/releases/download/${version}/${ASSET_NAME}"
    local tmp_file="/tmp/${ASSET_NAME}.download.$$"

    step "下载 ${ASSET_NAME} (版本 ${version})"
    info "URL: ${download_url}"

    if ! curl -fSL --progress-bar -o "$tmp_file" "$download_url"; then
        error "下载失败。可能该版本未提供 ${PLATFORM} 二进制。"
        error "请前往 https://github.com/${REPO}/releases 查看可用资产"
        rm -f "$tmp_file"
        exit 1
    fi

    # 校验为可执行文件（非 HTML 错误页）
    local file_type
    file_type="$(file -b "$tmp_file" 2>/dev/null || echo "unknown")"
    if echo "$file_type" | grep -qi "HTML"; then
        error "下载内容是 HTML 而非二进制，可能版本号或资产名错误"
        head -c 200 "$tmp_file" >&2
        rm -f "$tmp_file"
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
install_service() {
    local target="${INSTALL_DIR}/${BINARY_NAME}"
    local svc_path="/etc/systemd/system/${BINARY_NAME}.service"
    local web_port web_host auth

    step "配置 systemd 服务"
    echo "请回答以下问题（直接回车使用默认值）："
    read -rp "WEB 监听地址 [127.0.0.1]: " web_host
    web_host="${web_host:-127.0.0.1}"
    read -rp "WEB 监听端口 [8080]: " web_port
    web_port="${web_port:-8080}"
    read -rp "访问密码（user:password，留空无密码）: " auth

    local exec_args="--mode=web --web-host=${web_host} --web-port=${web_port}"
    if [[ -n "$auth" ]]; then
        exec_args="${exec_args} --auth=${auth}"
    fi

    cat > "$svc_path" <<EOF
[Unit]
Description=SSH Tunnel Manager (WEB mode)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=${target} ${exec_args}
Restart=on-failure
RestartSec=5
# 配置文件存放于用户配置目录，无需特殊权限
AmbientCapabilities=

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable "${BINARY_NAME}.service"
    info "服务已安装: ${svc_path}"
    info "启动: sudo systemctl start ${BINARY_NAME}"
    info "状态: sudo systemctl status ${BINARY_NAME}"
    info "日志: sudo journalctl -u ${BINARY_NAME} -f"

    read -rp "立即启动服务？[Y/n]: " start_now
    if [[ "${start_now:-Y}" =~ ^[Yy]$ ]]; then
        systemctl start "${BINARY_NAME}"
        sleep 1
        systemctl --no-pager status "${BINARY_NAME}" || true
    fi
}

# ==================== 主流程 ====================
main() {
    echo "${BOLD}================ sshsuidao 安装程序 ================${NC}"

    step "检测系统架构"
    detect_platform
    info "平台: ${PLATFORM}"
    info "资产名: ${ASSET_NAME}"

    # 确定版本
    if [[ -n "$TARGET_VERSION" ]]; then
        INSTALLED_VERSION="$TARGET_VERSION"
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

    # 可选：systemd 服务
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
        info "安装为服务: sudo bash install.sh -s -v ${INSTALLED_VERSION}"
    fi
}

main "$@"
