#!/bin/bash
# SSH Tunnel Manager (sshsuidao) Linux systemd 服务安装脚本
#
# 功能：
#   - 自动检测系统架构（amd64/arm64）
#   - 不带命令执行时进入交互式菜单（安装/卸载/查看状态）
#   - 交互式配置监听端口、用户名、密码
#   - 检测端口是否被占用
#   - 安装二进制到 /usr/local/bin
#   - 创建专用系统用户与配置目录
#   - 注册 systemd 服务（开机自启）
#
# 使用方式：
#   sudo ./install.sh              # 进入交互菜单
#   sudo ./install.sh install      # 直接安装并启动
#   sudo ./install.sh uninstall    # 停止并卸载
#   sudo ./install.sh status       # 查看服务状态
#   sudo ./install.sh restart      # 重启服务
#   sudo ./install.sh logs         # 查看实时日志

set -e

# ==================== 配置 ====================
APP_NAME="SSHTunnel"
APP_DESC="SSH Tunnel Manager"
INSTALL_DIR="/usr/local/bin"
CONFIG_DIR="/etc/${APP_NAME}"
DATA_DIR="/var/lib/${APP_NAME}"
LOG_DIR="/var/log/${APP_NAME}"
SERVICE_FILE="/etc/systemd/system/${APP_NAME}.service"
SERVICE_USER="${APP_NAME}"

# 运行参数默认值（WEB 模式 + 密码保护 + 监听 0.0.0.0）
DEFAULT_HOST="0.0.0.0"
DEFAULT_PORT="8090"
DEFAULT_USER="admin"
DEFAULT_PASS="admin123"

# 运行时变量（由交互输入或环境变量填充）
LISTEN_HOST="${LISTEN_HOST:-$DEFAULT_HOST}"
LISTEN_PORT="${LISTEN_PORT:-$DEFAULT_PORT}"
AUTH_USER="${AUTH_USER:-$DEFAULT_USER}"
AUTH_PASS="${AUTH_PASS:-$DEFAULT_PASS}"

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${GREEN}[INFO]${NC}  $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; exit 1; }
prompt(){ echo -e "${CYAN}[?]${NC} $*"; }

# ==================== 权限检查 ====================
check_root() {
    if [[ $EUID -ne 0 ]]; then
        error "此脚本必须以 root 权限运行，请使用 sudo 或切换到 root 用户"
    fi
}

# ==================== 端口检测 ====================
# 检测端口是否被占用
# 返回: 0=可用, 1=被占用
port_is_available() {
    local port="$1"

    # 优先使用 ss
    if command -v ss >/dev/null 2>&1; then
        if ss -tln 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${port}$"; then
            return 1
        fi
        return 0
    fi

    # 退而使用 netstat
    if command -v netstat >/dev/null 2>&1; then
        if netstat -tln 2>/dev/null | awk '{print $4}' | grep -qE "[:.]${port}$"; then
            return 1
        fi
        return 0
    fi

    # 退而使用 /proc/net/tcp（仅支持十进制端口检测）
    local hex_port
    hex_port=$(printf '%04X' "$port" 2>/dev/null)
    if [[ -n "$hex_port" ]] && grep -qE "^[0-9A-Fa-f]+:[0-9A-Fa-f]{4}:[0-9A-Fa-f]+ " /proc/net/tcp /proc/net/tcp6 2>/dev/null; then
        if awk '{print $2}' /proc/net/tcp /proc/net/tcp6 2>/dev/null | grep -qE ":[0-9A-Fa-f]+:${hex_port}$"; then
            return 1
        fi
    fi
    return 0
}

# 查找占用端口的进程信息
port_occupant_info() {
    local port="$1"
    if command -v ss >/dev/null 2>&1; then
        ss -tlnp 2>/dev/null | grep -E "[:.]${port}\s" | head -5
    elif command -v netstat >/dev/null 2>&1; then
        netstat -tlnp 2>/dev/null | grep -E "[:.]${port}\s" | head -5
    fi
}

# ==================== 交互式收集参数 ====================
collect_params_interactively() {
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}  ${APP_DESC} 安装配置${NC}"
    echo -e "${BLUE}========================================${NC}"
    echo -e "  运行模式: WEB 面板（密码保护）"
    echo -e "  监听地址: ${DEFAULT_HOST}（固定，监听所有网卡）"
    echo ""

    # ---------- 1. 监听端口 ----------
    while true; do
        echo ""
        prompt "请输入监听端口 [默认 ${DEFAULT_PORT}]: "
        read -r input_port
        input_port="${input_port:-$DEFAULT_PORT}"

        # 校验是否为数字且范围合法
        if ! [[ "$input_port" =~ ^[0-9]+$ ]]; then
            warn "端口必须为数字，请重新输入"
            continue
        fi
        if (( input_port < 1 || input_port > 65535 )); then
            warn "端口范围应为 1-65535，请重新输入"
            continue
        fi

        # 检测端口占用
        if port_is_available "$input_port"; then
            LISTEN_PORT="$input_port"
            info "端口 ${input_port} 可用"
            break
        else
            warn "端口 ${input_port} 已被占用，占用情况如下："
            port_occupant_info "$input_port" | sed 's/^/      /'
            echo ""
            prompt "是否换一个端口？[Y/n] (Y=重新输入, n=强制使用此端口): "
            read -r force_use
            force_use="${force_use:-Y}"
            if [[ "$force_use" =~ ^[Nn]$ ]]; then
                LISTEN_PORT="$input_port"
                warn "将强制使用端口 ${input_port}（可能导致服务启动失败）"
                break
            fi
            # 继续循环让用户重新输入
        fi
    done

    # ---------- 2. 用户名 ----------
    echo ""
    while true; do
        prompt "请输入登录用户名 [默认 ${DEFAULT_USER}]: "
        read -r input_user
        input_user="${input_user:-$DEFAULT_USER}"

        if [[ -z "$input_user" ]]; then
            warn "用户名不能为空"
            continue
        fi
        # 简单校验：不允许包含冒号（与 --auth=user:pass 格式冲突）
        if [[ "$input_user" == *:* ]]; then
            warn "用户名不能包含冒号 ':'"
            continue
        fi
        AUTH_USER="$input_user"
        break
    done

    # ---------- 3. 密码 ----------
    echo ""
    while true; do
        prompt "请输入登录密码 [默认 ${DEFAULT_PASS}] (输入后将隐藏显示): "
        read -r -s input_pass
        echo ""
        input_pass="${input_pass:-$DEFAULT_PASS}"

        if [[ -z "$input_pass" ]]; then
            warn "密码不能为空"
            continue
        fi
        # 校验长度
        if (( ${#input_pass} < 6 )); then
            warn "密码长度至少 6 位，请重新输入"
            continue
        fi
        # 二次确认
        prompt "请再次输入密码以确认: "
        read -r -s input_pass2
        echo ""
        if [[ "$input_pass" != "$input_pass2" ]]; then
            warn "两次输入的密码不一致，请重新输入"
            continue
        fi
        AUTH_PASS="$input_pass"
        break
    done

    # ---------- 4. 确认配置 ----------
    echo ""
    echo -e "${BLUE}========================================${NC}"
    echo -e "${BLUE}  配置确认${NC}"
    echo -e "${BLUE}========================================${NC}"
    echo -e "  运行模式:  WEB 面板（密码保护）"
    echo -e "  监听地址:  ${DEFAULT_HOST}"
    echo -e "  监听端口:  ${GREEN}${LISTEN_PORT}${NC}"
    echo -e "  登录账户:  ${GREEN}${AUTH_USER}${NC}"
    echo -e "  登录密码:  ${GREEN}$(printf '*%.0s' $(seq 1 ${#AUTH_PASS}))${NC}（${#AUTH_PASS} 位）"
    echo -e "${BLUE}========================================${NC}"
    echo ""
    prompt "确认以上配置并开始安装？[Y/n]: "
    read -r confirm
    confirm="${confirm:-Y}"
    if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
        warn "已取消安装"
        exit 0
    fi
}

# ==================== 架构检测 ====================
detect_arch() {
    local arch
    arch=$(uname -m)
    case "$arch" in
        x86_64|amd64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            error "不支持的系统架构: $arch（仅支持 amd64/arm64）"
            ;;
    esac
}

# 查找脚本所在目录的二进制文件
find_binary() {
    local arch="$1"
    local script_dir
    script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

    # 按优先级查找候选二进制
    local candidates=(
        "${script_dir}/sshsuidao-linux-${arch}"
        "${script_dir}/build/bin/sshsuidao-linux-${arch}"
        "${script_dir}/bin/sshsuidao-linux-${arch}"
        "${script_dir}/${APP_NAME}"
    )

    for bin in "${candidates[@]}"; do
        if [[ -f "$bin" && -x "$bin" ]]; then
            echo "$bin"
            return 0
        fi
    done
    return 1
}

# ==================== 安装 ====================
install_service() {
    info "开始安装 ${APP_DESC} (${APP_NAME})..."

    # 交互式收集参数
    collect_params_interactively

    local arch
    arch=$(detect_arch)
    info "检测到系统架构: ${arch}"

    # 查找二进制文件
    local bin_src
    if ! bin_src=$(find_binary "$arch"); then
        error "未找到 Linux ${arch} 二进制文件

请先将 sshsuidao-linux-${arch} 放到以下任一位置：
  - 脚本同目录
  - ./build/bin/
  - ./bin/

或先在源码目录执行交叉编译：
  GOOS=linux GOARCH=${arch} CGO_ENABLED=0 go build -o sshsuidao-linux-${arch} ."
    fi
    info "使用二进制文件: ${bin_src}"

    # 1. 创建系统用户（无登录权限）
    if ! id "$SERVICE_USER" &>/dev/null; then
        info "创建系统用户: ${SERVICE_USER}"
        useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
    else
        info "系统用户已存在: ${SERVICE_USER}"
    fi

    # 2. 创建目录
    info "创建目录..."
    install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0750 "$CONFIG_DIR"
    install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0750 "$DATA_DIR"
    install -d -o "$SERVICE_USER" -g "$SERVICE_USER" -m 0750 "$LOG_DIR"

    # 3. 安装二进制
    info "安装二进制到 ${INSTALL_DIR}/${APP_NAME}"
    install -o root -g root -m 0755 "$bin_src" "${INSTALL_DIR}/${APP_NAME}"

    # 4. 安装前再次检测端口（防止用户在确认后被其他进程占用）
    if ! port_is_available "$LISTEN_PORT"; then
        warn "端口 ${LISTEN_PORT} 在确认后被占用："
        port_occupant_info "$LISTEN_PORT" | sed 's/^/      /'
        prompt "是否继续安装？[y/N]: "
        read -r force_continue
        if [[ ! "$force_continue" =~ ^[Yy]$ ]]; then
            error "已取消安装"
        fi
        warn "继续安装（服务可能启动失败）"
    fi

    # 5. 写入 systemd 服务单元
    info "写入 systemd 服务文件: ${SERVICE_FILE}"
    cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=${APP_DESC} (WEB Panel)
Documentation=https://github.com/sshsuidao/sshsuidao
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}

# 运行参数：WEB 模式 + 密码保护 + 监听 ${LISTEN_HOST}
ExecStart=${INSTALL_DIR}/${APP_NAME} \\
    --mode=web \\
    --web-host=${LISTEN_HOST} \\
    --web-port=${LISTEN_PORT} \\
    --auth=${AUTH_USER}:${AUTH_PASS}

# 工作目录与配置
WorkingDirectory=${DATA_DIR}
Environment=XDG_CONFIG_HOME=${CONFIG_DIR}
Environment=XDG_CACHE_HOME=${DATA_DIR}
Environment=XDG_DATA_HOME=${DATA_DIR}

# 重启策略
Restart=on-failure
RestartSec=5s
StartLimitBurst=3
StartLimitIntervalSec=60

# 资源与安全限制
LimitNOFILE=65536
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=${CONFIG_DIR} ${DATA_DIR} ${LOG_DIR}

# 标准输出/错误重定向到 journal
StandardOutput=journal
StandardError=journal
SyslogIdentifier=${APP_NAME}

[Install]
WantedBy=multi-user.target
EOF

    # 6. 重新加载 systemd 配置
    info "重新加载 systemd 配置..."
    systemctl daemon-reload

    # 7. 启用开机自启
    info "启用开机自启..."
    systemctl enable "${APP_NAME}.service" >/dev/null 2>&1

    # 8. 启动服务
    info "启动服务..."
    systemctl start "${APP_NAME}.service" || {
        error "服务启动失败，请查看日志：journalctl -u ${APP_NAME} -e"
    }

    # 9. 等待启动并检查状态
    sleep 2
    if systemctl is-active --quiet "${APP_NAME}.service"; then
        info ""
        info "===================================="
        info "  ${APP_DESC} 安装成功！"
        info "===================================="
        info ""
        info "  访问地址:  http://<服务器IP>:${LISTEN_PORT}"
        info "  登录账户:  ${AUTH_USER}"
        info "  登录密码:  ${AUTH_PASS}"
        info ""
        info "  配置文件:  ${CONFIG_DIR}/configs.json"
        info "  日志目录:  ${LOG_DIR}/"
        info "  服务文件:  ${SERVICE_FILE}"
        info ""
        info "  常用命令:"
        info "    systemctl status  ${APP_NAME}    # 查看状态"
        info "    systemctl restart ${APP_NAME}    # 重启服务"
        info "    systemctl stop    ${APP_NAME}    # 停止服务"
        info "    journalctl -u ${APP_NAME} -f     # 实时日志"
        info ""
        warn "  ⚠️  默认监听 ${LISTEN_HOST}，请确保防火墙已开放端口 ${LISTEN_PORT} 给可信来源。"
        warn "  ⚠️  建议在反向代理（如 Nginx）后启用 TLS 以保护密码传输。"
        echo ""
    else
        error "服务启动失败，请查看日志：journalctl -u ${APP_NAME} -e"
    fi
}

# ==================== 卸载 ====================
uninstall_service() {
    info "开始卸载 ${APP_DESC}..."

    # 1. 停止服务
    if systemctl is-active --quiet "${APP_NAME}.service"; then
        info "停止服务..."
        systemctl stop "${APP_NAME}.service" || true
    else
        info "服务未运行"
    fi

    # 2. 禁用开机自启
    if systemctl is-enabled --quiet "${APP_NAME}.service" 2>/dev/null; then
        info "禁用开机自启..."
        systemctl disable "${APP_NAME}.service" || true
    fi

    # 3. 删除服务文件
    if [[ -f "$SERVICE_FILE" ]]; then
        info "删除服务文件: ${SERVICE_FILE}"
        rm -f "$SERVICE_FILE"
    fi

    # 4. 重新加载 systemd 配置
    info "重新加载 systemd 配置..."
    systemctl daemon-reload
    systemctl reset-failed "${APP_NAME}.service" 2>/dev/null || true

    # 5. 删除二进制
    if [[ -f "${INSTALL_DIR}/${APP_NAME}" ]]; then
        info "删除二进制: ${INSTALL_DIR}/${APP_NAME}"
        rm -f "${INSTALL_DIR}/${APP_NAME}"
    fi

    # 6. 询问是否删除配置/数据
    echo ""
    warn "是否删除配置与数据目录？此操作不可恢复！"
    warn "  配置目录: ${CONFIG_DIR}"
    warn "  数据目录: ${DATA_DIR}"
    warn "  日志目录: ${LOG_DIR}"
    read -r -p "确认删除？[y/N]: " confirm
    if [[ "$confirm" =~ ^[Yy]$ ]]; then
        info "删除配置与数据目录..."
        rm -rf "$CONFIG_DIR" "$DATA_DIR" "$LOG_DIR"
        info "已删除"
    else
        info "保留配置与数据目录"
    fi

    # 7. 询问是否删除系统用户
    if id "$SERVICE_USER" &>/dev/null; then
        read -r -p "是否删除系统用户 ${SERVICE_USER}？[y/N]: " confirm_user
        if [[ "$confirm_user" =~ ^[Yy]$ ]]; then
            userdel "$SERVICE_USER" 2>/dev/null || true
            info "已删除系统用户: ${SERVICE_USER}"
        else
            info "保留系统用户: ${SERVICE_USER}"
        fi
    fi

    info ""
    info "${APP_DESC} 已卸载完成"
}

# ==================== 状态查询 ====================
status_service() {
    info "服务状态: ${APP_NAME}"
    echo ""
    systemctl status "${APP_NAME}.service" --no-pager -l || true
    echo ""

    # 解析服务文件中的端口
    local svc_port
    svc_port=$(grep -oE '\-\-web-port=[0-9]+' "$SERVICE_FILE" 2>/dev/null | grep -oE '[0-9]+' || echo "$DEFAULT_PORT")

    # 检测端口监听
    if command -v ss >/dev/null 2>&1; then
        info "端口监听情况 (端口 ${svc_port}):"
        ss -tlnp 2>/dev/null | grep -E "[:.]${svc_port}\s" || warn "未检测到端口 ${svc_port} 监听"
    fi
}

# ==================== 重启 ====================
restart_service() {
    info "重启服务: ${APP_NAME}"
    systemctl restart "${APP_NAME}.service"
    sleep 1
    if systemctl is-active --quiet "${APP_NAME}.service"; then
        info "服务已重启"
    else
        error "服务重启失败"
    fi
}

# ==================== 实时日志 ====================
view_logs() {
    info "实时日志（Ctrl+C 退出）: ${APP_NAME}"
    journalctl -u "${APP_NAME}.service" -f --no-pager
}

# ==================== 交互式主菜单 ====================
show_menu() {
    echo ""
    echo -e "${BLUE}================================================${NC}"
    echo -e "${BLUE}   ${APP_DESC} 服务管理工具${NC}"
    echo -e "${BLUE}================================================${NC}"
    echo ""
    # 显示当前服务状态摘要
    if systemctl is-active --quiet "${APP_NAME}.service" 2>/dev/null; then
        echo -e "  当前状态: ${GREEN}● 运行中${NC}"
    elif systemctl is-enabled --quiet "${APP_NAME}.service" 2>/dev/null; then
        echo -e "  当前状态: ${YELLOW}● 已停止（已设为开机自启）${NC}"
    elif [[ -f "$SERVICE_FILE" ]]; then
        echo -e "  当前状态: ${YELLOW}● 已安装但未启用${NC}"
    else
        echo -e "  当前状态: ${RED}○ 未安装${NC}"
    fi
    echo ""
    echo -e "  ${GREEN}1)${NC} 安装 ${APP_DESC}"
    echo -e "  ${RED}2)${NC} 卸载 ${APP_DESC}"
    echo -e "  ${CYAN}3)${NC} 查看 ${APP_DESC} 状态"
    echo -e "  ${YELLOW}4)${NC} 重启 ${APP_DESC}"
    echo -e "  ${YELLOW}5)${NC} 查看实时日志"
    echo -e "  ${RED}0)${NC} 退出"
    echo ""
    echo -e "${BLUE}------------------------------------------------${NC}"
}

# 交互式主循环
interactive_menu() {
    while true; do
        show_menu
        prompt "请选择操作 [0-5]: "
        read -r choice
        case "$choice" in
            1)
                echo ""
                install_service
                echo ""
                prompt "按回车键返回菜单..."
                read -r
                ;;
            2)
                echo ""
                uninstall_service
                echo ""
                prompt "按回车键返回菜单..."
                read -r
                ;;
            3)
                echo ""
                status_service
                echo ""
                prompt "按回车键返回菜单..."
                read -r
                ;;
            4)
                echo ""
                restart_service
                echo ""
                prompt "按回车键返回菜单..."
                read -r
                ;;
            5)
                echo ""
                view_logs
                echo ""
                prompt "按回车键返回菜单..."
                read -r
                ;;
            0)
                info "已退出"
                exit 0
                ;;
            "")
                continue
                ;;
            *)
                warn "无效选择: $choice，请重新输入"
                ;;
        esac
    done
}

# ==================== 使用说明 ====================
usage() {
    cat <<EOF
${APP_DESC} Linux 服务管理脚本

用法:
    sudo $0 [命令]

不带命令执行时进入交互式菜单（推荐）。

命令:
    install      交互式安装并启动服务（输入端口、账户、密码，自动检测端口占用）
    uninstall    停止并卸载服务（可交互式选择保留配置）
    status       查看服务运行状态与端口监听
    restart      重启服务
    logs         实时查看日志（journalctl -f）
    help         显示此帮助

非交互模式（通过环境变量传参，跳过交互输入）:
    LISTEN_PORT=8443 AUTH_USER=manager AUTH_PASS=Str0ngPwd sudo -E $0 install

默认值:
    监听地址:  0.0.0.0（固定）
    监听端口:  8090
    登录账户:  admin
    登录密码:  admin123

示例:
    # 进入交互菜单（推荐）
    sudo $0

    # 交互式安装
    sudo $0 install

    # 非交互模式安装（CI/自动化场景）
    LISTEN_PORT=8443 AUTH_USER=admin AUTH_PASS=MyPass123 sudo -E $0 install

    # 卸载
    sudo $0 uninstall

    # 查看状态
    sudo $0 status

    # 查看日志
    sudo $0 logs

EOF
}

# ==================== 主入口 ====================
main() {
    local cmd="${1:-}"

    case "$cmd" in
        install)
            check_root
            install_service
            ;;
        uninstall)
            check_root
            uninstall_service
            ;;
        status)
            check_root
            status_service
            ;;
        restart)
            check_root
            restart_service
            ;;
        logs)
            check_root
            view_logs
            ;;
        -h|--help|help)
            usage
            ;;
        "")
            # 无命令：进入交互式菜单
            check_root
            interactive_menu
            ;;
        *)
            error "未知命令: $cmd
运行 '$0 --help' 查看可用命令"
            ;;
    esac
}

main "$@"
