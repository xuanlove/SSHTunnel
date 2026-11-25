#!/usr/bin/env bash
#
# sshtunnel Linux 安装脚本
#
# 功能：
#   1. 自动检测系统架构（amd64 / arm64）
#   2. 从 GitHub Release 获取最新版本号（多源重试）
#   3. 多镜像测速后从最快的源下载二进制，失败自动切换下一个源
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
# 镜像相关环境变量：
#   SSHTUNNEL_MIRRORS       追加 GitHub 代理（空格分隔，如 "https://ghfast.top https://ghproxy.net"）
#   SSHTUNNEL_DIRECT=0      不使用 GitHub 直连，只用镜像
#   SSHTUNNEL_MIRROR_TEST=0 跳过测速，按内置顺序尝试
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

# ==================== 下载镜像 ====================
# GitHub 代理根（空串表示直连）。实际 URL = ${proxy:+$proxy/}https://github.com/...
MIRROR_PROXIES=(
    ""
    "https://ghfast.top"
    "https://ghproxy.net"
    "https://gh-proxy.com"
    "https://github.moeyy.xyz"
)
# 不在默认列表中展示直连（如纯内网只能走自建镜像）
if [[ "${SSHTUNNEL_DIRECT:-1}" == "0" ]]; then
    MIRROR_PROXIES=("${MIRROR_PROXIES[@]:1}")
fi
# 追加用户自定义镜像（去掉尾部斜杠，避免拼出双斜杠 URL）
if [[ -n "${SSHTUNNEL_MIRRORS:-}" ]]; then
    read -r -a _extra_mirrors <<<"${SSHTUNNEL_MIRRORS}"
    if [[ ${#_extra_mirrors[@]} -gt 0 ]]; then
        for _m in "${_extra_mirrors[@]}"; do
            [[ -z "$_m" ]] && continue
            MIRROR_PROXIES+=("${_m%/}")
        done
        unset _m
    fi
    unset _extra_mirrors
fi
# 关闭测速：不测速，按上面的列表顺序依次尝试
SKIP_MIRROR_TEST=false
if [[ "${SSHTUNNEL_MIRROR_TEST:-1}" == "0" ]]; then
    SKIP_MIRROR_TEST=true
fi

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

${BOLD}镜像相关环境变量:${NC}
  SSHTUNNEL_MIRRORS        追加 GitHub 代理根（空格分隔）
                           例: SSHTUNNEL_MIRRORS="https://ghfast.top https://ghproxy.net"
  SSHTUNNEL_DIRECT=0       不使用 GitHub 直连，只用镜像
  SSHTUNNEL_MIRROR_TEST=0  跳过测速，按内置顺序尝试

默认会对直连与内置镜像并行测速，从最快的源下载，失败自动切换下一个源。

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

# ==================== 镜像 URL ====================
# release_url 拼出资产下载地址；proxy 为空表示 GitHub 直连
release_url() {
    local proxy="$1" version="$2" file="$3"
    local base="https://github.com/${REPO}/releases/download/${version}/${file}"
    if [[ -n "$proxy" ]]; then
        echo "${proxy}/${base}"
    else
        echo "$base"
    fi
}

# latest_url 拼出「最新版本」重定向地址
latest_url() {
    local proxy="$1"
    local base="https://github.com/${REPO}/releases/latest"
    if [[ -n "$proxy" ]]; then
        echo "${proxy}/${base}"
    else
        echo "$base"
    fi
}

# 镜像可读名称（用于日志）
mirror_label() {
    local proxy="$1"
    if [[ -z "$proxy" ]]; then
        echo "GitHub 直连"
    else
        echo "${proxy#*://}"
    fi
}

# 字节/秒转可读速度
human_speed() {
    awk -v s="${1:-0}" 'BEGIN {
        if (s <= 0) { print "未知"; exit }
        if (s >= 1048576) printf "%.1f MB/s", s / 1048576
        else if (s >= 1024) printf "%.0f KB/s", s / 1024
        else printf "%.0f B/s", s
    }'
}

# bench_mirrors 并行测速，按推荐顺序在 stdout 输出代理根（每行一个，空行=直连）。
# 策略：对每个镜像 Range 取前 512KB 实测吞吐（--max-filesize 防止不支持 Range
# 的镜像下载整个文件），按吞吐降序；吞吐未知时用 TTFB 升序兜底。
bench_mirrors() {
    local version="$1"
    local dir
    dir="$(mktemp -d)"
    local i proxy

    for i in "${!MIRROR_PROXIES[@]}"; do
        proxy="${MIRROR_PROXIES[$i]}"
        (
            local url out
            url="$(release_url "$proxy" "$version" "$ASSET_NAME")"
            out="$(curl -sS -o /dev/null --connect-timeout 4 --max-time 10 \
                --max-filesize 3145728 -r 0-524287 \
                -w '%{http_code} %{time_total} %{speed_download}' \
                "$url" 2>/dev/null || true)"
            printf '%s\n' "${out:-000 99 0}" >"$dir/probe.$i"
        ) &
    done
    wait 2>/dev/null || true

    # 汇总可用镜像：code<TAB>ttfb<TAB>speed<TAB>index
    : >"$dir/all"
    for i in "${!MIRROR_PROXIES[@]}"; do
        local line code ttfb speed
        line="$(cat "$dir/probe.$i" 2>/dev/null || true)"
        code="000"; ttfb="99"; speed="0"
        read -r code ttfb speed _ <<<"${line:-000 99 0}" || true
        [[ "$code" == "200" || "$code" == "206" ]] || continue
        printf '%s\t%s\t%s\t%s\n' "$code" "$ttfb" "$speed" "$i" >>"$dir/all"
    done

    if [[ ! -s "$dir/all" ]]; then
        rm -rf "$dir"
        return 0
    fi

    local ranked
    ranked="$(
        {
            awk -F'\t' '$3+0 > 0' "$dir/all" | sort -t$'\t' -k3,3nr -k2,2g | cut -f4
            awk -F'\t' '$3+0 == 0' "$dir/all" | sort -t$'\t' -k2,2g | cut -f4
        }
    )"

    info "镜像测速结果（按下载速度排序）："
    local idx n=0
    while IFS= read -r idx; do
        [[ -z "$idx" ]] && continue
        n=$((n + 1))
        local spd
        spd="$(awk -F'\t' -v k="$idx" '$4==k {print $3}' "$dir/all" | head -1)"
        info "  ${n}. $(mirror_label "${MIRROR_PROXIES[$idx]}") · $(human_speed "$spd")"
    done <<<"$ranked"

    while IFS= read -r idx; do
        [[ -z "$idx" ]] && continue
        printf '%s\n' "${MIRROR_PROXIES[$idx]}"
    done <<<"$ranked"

    rm -rf "$dir"
}

# ==================== 获取最新版本 ====================
# 利用 GitHub /releases/latest 的 302 重定向解析最新 tag，无需 jq。
# 相比 api.github.com 不受未认证请求限流影响。
# 直连与各镜像依次尝试，任一成功即返回。
get_latest_version() {
    local proxy url final_url tag
    for proxy in "${MIRROR_PROXIES[@]}"; do
        url="$(latest_url "$proxy")"
        # -I 仅取头，-o 输出到文件，-w 输出最终 URL
        final_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
            --connect-timeout 5 --max-time 15 "$url" 2>/dev/null || true)"
        if [[ -z "$final_url" ]]; then
            continue
        fi
        # 最终 URL 形如 https://github.com/owner/repo/releases/tag/v1.0.0
        tag="${final_url##*/tag/}"
        if [[ -n "$tag" && "$tag" != "$final_url" ]]; then
            echo "$tag"
            return 0
        fi
    done
    error "无法获取最新版本（网络问题或仓库 ${REPO} 无 Release）"
    error "可检查网络，或用 -v 指定版本、通过 SSHTUNNEL_MIRRORS 追加可用镜像"
    exit 1
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
    # 使用私有临时目录，避免可预测路径被本地用户预建符号链接利用
    local tmp_dir
    tmp_dir="$(mktemp -d)"
    local tmp_file="${tmp_dir}/${ASSET_NAME}"

    # 多镜像测速，得到按速度排序的下载源列表（SSHTUNNEL_MIRROR_TEST=0 时按配置顺序）
    local -a order=()
    if $SKIP_MIRROR_TEST; then
        step "使用配置顺序试下载（已跳过测速）"
        order=("${MIRROR_PROXIES[@]}")
    else
        step "测速选择下载源"
        mapfile -t order < <(bench_mirrors "$version")
    fi
    if [[ ${#order[@]} -eq 0 ]]; then
        error "所有下载源均不可用（已尝试 ${#MIRROR_PROXIES[@]} 个源）"
        error "请检查网络，或通过 SSHTUNNEL_MIRRORS 追加可用镜像后重试"
        rm -rf "$tmp_dir"
        exit 1
    fi

    step "下载 ${ASSET_NAME} (版本 ${version})"
    local downloaded=false chosen_url="" i proxy url
    for i in "${!order[@]}"; do
        proxy="${order[$i]}"
        url="$(release_url "$proxy" "$version" "$ASSET_NAME")"
        info "[$((i + 1))/${#order[@]}] $(mirror_label "$proxy")"
        info "URL: ${url}"
        if curl -fSL --progress-bar --connect-timeout 10 --max-time 900 \
            -o "$tmp_file" "$url"; then
            # 有些代理会用 200 返回 HTML 错误页，需排除
            if head -c 512 "$tmp_file" 2>/dev/null | grep -qi '<html'; then
                warn "下载内容是 HTML 错误页，切换下一个源"
                continue
            fi
            downloaded=true
            chosen_url="$url"
            break
        fi
        warn "该源下载失败，切换下一个源"
    done

    if ! $downloaded; then
        error "下载失败：所有源均未成功"
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

    # 校验 sha256（发布资产附带同名 .sha256；缺失时兼容旧版本）
    local checksum_file="${tmp_file}.sha256"
    local sum_ok=false
    command -v sha256sum >/dev/null 2>&1 && sum_ok=true
    command -v shasum >/dev/null 2>&1 && sum_ok=true
    if ! $sum_ok; then
        warn "未找到 sha256sum/shasum，跳过完整性校验"
    else
        # 先走当前下载源，失败再回退直连
        if ! curl -fsSL --connect-timeout 8 --max-time 30 -o "$checksum_file" \
            "${chosen_url}.sha256" 2>/dev/null; then
            curl -fsSL --connect-timeout 8 --max-time 30 -o "$checksum_file" \
                "$(release_url "" "$version" "${ASSET_NAME}.sha256")" 2>/dev/null || true
        fi
        if [[ -s "$checksum_file" ]]; then
            local expected actual
            expected="$(awk '{print $1}' "$checksum_file" | head -1)"
            if command -v sha256sum >/dev/null 2>&1; then
                actual="$(sha256sum "$tmp_file" | awk '{print $1}')"
            else
                actual="$(shasum -a 256 "$tmp_file" | awk '{print $1}')"
            fi
            if [[ -z "$expected" || "$expected" != "$actual" ]]; then
                error "sha256 校验失败，二进制可能被篡改或下载不完整"
                rm -rf "$tmp_dir"
                exit 1
            fi
            info "sha256 校验通过"
        else
            warn "未提供 sha256 校验和，跳过完整性校验"
        fi
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
        generated_pass="$(LC_ALL=C od -An -N12 -tx1 /dev/urandom | tr -d '[:space:]')"
        if [[ ${#generated_pass} -ne 24 ]]; then
            error "生成随机访问密码失败"
            exit 1
        fi
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
        read -rsp "访问密码（user:password，留空使用默认密码）: " input; echo >&2; auth="${input:-$auth}"
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

    if $INSTALL_SERVICE; then
        if $UPGRADE_MODE; then
            local service="${BINARY_NAME}.service"
            if [[ "$(systemctl show --property=LoadState --value "$service")" == "loaded" ]]; then
                systemctl try-restart "$service"
            fi
        else
            install_service
        fi
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
