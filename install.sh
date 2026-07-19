#!/usr/bin/env bash
# install.sh — XiaoyuPostHub 安装/更新脚本
#
# 用法：
#   ./install.sh                     安装或更新
#   ./install.sh --logs              查看日志
#   ./install.sh --status            查看状态
#   ./install.sh --stop              停止服务，保留数据
#   ./install.sh --restart           重启服务
#   ./install.sh --uninstall         卸载，保留数据卷
#   ./install.sh --uninstall --purge 卸载并删除数据卷
#
# 环境变量：
#   XIAOYUPOSTHUB_HOME=/srv/xiaoyuposthub ./install.sh
#   XIAOYUPOSTHUB_IMAGE=pylintech/xiaoyuposthub:v1.0.0 ./install.sh
#   XIAOYUPOSTHUB_NETWORK=1panel-network ./install.sh

set -Eeuo pipefail

APP_NAME="XiaoyuPostHub"
PROJECT_NAME="xiaoyuposthub"
CONTAINER_NAME="XiaoyuPostHub"
IMAGE_DEFAULT="pylintech/xiaoyuposthub:latest"
PORT_DEFAULT="8080"
NETWORK_DEFAULT="xiaoyuposthub-network"
MIGRATION_POSTGRES_IMAGE="${XPH_MIGRATION_POSTGRES_IMAGE:-postgres:18-alpine}"

INSTALL_DIR="${XIAOYUPOSTHUB_HOME:-/opt/xiaoyuposthub}"
COMPOSE_FILE="${INSTALL_DIR}/compose.yaml"
ENV_FILE="${INSTALL_DIR}/.env"
MIGRATION_ASSISTANT_FILE="${INSTALL_DIR}/migration-assistant.sh"

ACTION="install"
ACTION_SET=false
PURGE=false
DISPLAY_ADMIN_PASSWORD=""

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    BLUE=$'\033[34m'
    GREEN=$'\033[32m'
    YELLOW=$'\033[33m'
    RED=$'\033[31m'
    RESET=$'\033[0m'
else
    BLUE=""
    GREEN=""
    YELLOW=""
    RED=""
    RESET=""
fi

usage() {
    cat <<EOF_USAGE
用法：
  ./install.sh                     安装或更新 ${APP_NAME}
  ./install.sh --logs              查看日志
  ./install.sh --status            查看状态
  ./install.sh --stop              停止服务
  ./install.sh --restart           重启服务
  ./install.sh --uninstall         卸载，保留数据卷
  ./install.sh --uninstall --purge 卸载并删除数据卷
  ./install.sh -h|--help           查看帮助

环境变量：
  XIAOYUPOSTHUB_HOME               安装目录，默认 ${INSTALL_DIR}
  XIAOYUPOSTHUB_IMAGE              镜像，默认 ${IMAGE_DEFAULT}
  XIAOYUPOSTHUB_NETWORK            Docker 网络；已存在时直接加入，不存在时创建
  XIAOYUPOSTHUB_NETWORK_EXTERNAL   true 使用已有网络，false 由 Compose 创建

默认值：
  访问端口：${PORT_DEFAULT}
  配置文件：${ENV_FILE}
EOF_USAGE
}

info() { printf "%b%s%b\n" "${BLUE}" "$*" "${RESET}"; }
ok() { printf "%b%s%b\n" "${GREEN}" "$*" "${RESET}"; }
warn() { printf "%b%s%b\n" "${YELLOW}" "$*" "${RESET}"; }
die() {
    printf "%b错误：%s%b\n" "${RED}" "$*" "${RESET}" >&2
    exit 1
}

run_step() {
    local title="$1"
    local log=""
    shift
    log="$(mktemp)"
    info "执行：${title}"
    if ! "$@" >"${log}" 2>&1; then
        printf "%b错误：%s失败%b\n" "${RED}" "${title}" "${RESET}" >&2
        sed 's/^/    /' "${log}" >&2
        rm -f "${log}"
        exit 1
    fi
    rm -f "${log}"
    ok "完成：${title}"
}

has_cmd() { command -v "$1" >/dev/null 2>&1; }

extract_migration_assistant() {
    local image="$1"
    local source_container=""
    local temp_dir=""
    local extracted_file=""

    temp_dir="$(mktemp -d)"
    extracted_file="${temp_dir}/migration-assistant.sh"
    if ! source_container="$(docker create --entrypoint /bin/true "${image}")"; then
        rmdir "${temp_dir}"
        return 1
    fi
    if ! docker cp "${source_container}:/app/migration-assistant.sh" "${extracted_file}" \
        || [[ ! -s "${extracted_file}" ]]; then
        docker rm -f "${source_container}" >/dev/null 2>&1 || true
        rm -f "${extracted_file}"
        rmdir "${temp_dir}"
        return 1
    fi
    docker rm -f "${source_container}" >/dev/null
    chmod 700 "${extracted_file}"
    mv "${extracted_file}" "${MIGRATION_ASSISTANT_FILE}"
    rmdir "${temp_dir}"
}

need_sudo() {
    [[ "$(id -u)" -ne 0 && ! -w "$(dirname "${INSTALL_DIR}")" ]]
}

run_privileged() {
    if need_sudo; then
        has_cmd sudo || die "没有 sudo 权限。请用 root 执行，或设置 XIAOYUPOSTHUB_HOME 到可写目录"
        sudo "$@"
    else
        "$@"
    fi
}

compose() {
    docker compose \
        --project-name "${PROJECT_NAME}" \
        --file "${COMPOSE_FILE}" \
        --project-directory "${INSTALL_DIR}" \
        "$@"
}

set_action() {
    local next_action="$1"

    if [[ "${ACTION_SET}" == true && "${ACTION}" != "${next_action}" ]]; then
        die "一次只能执行一个操作"
    fi

    ACTION="${next_action}"
    ACTION_SET=true
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --logs) set_action "logs" ;;
            --status) set_action "status" ;;
            --stop) set_action "stop" ;;
            --restart) set_action "restart" ;;
            --uninstall) set_action "uninstall" ;;
            --purge) PURGE=true ;;
            -h|--help) usage; exit 0 ;;
            *) die "未知参数：$1" ;;
        esac
        shift
    done

    if [[ "${PURGE}" == true && "${ACTION}" != "uninstall" ]]; then
        die "--purge 只能和 --uninstall 一起使用"
    fi
}

check_docker() {
    has_cmd docker || die "未安装 Docker"
    docker info >/dev/null 2>&1 || die "Docker 未运行"
    docker compose version >/dev/null 2>&1 || die "Docker Compose V2 不可用"
}

require_compose_file() {
    [[ -f "${COMPOSE_FILE}" ]] || die "未找到 ${COMPOSE_FILE}，请先运行安装"
}

prepare_install_dir() {
    if [[ ! -d "${INSTALL_DIR}" ]]; then
        run_step "创建安装目录" run_privileged mkdir -p "${INSTALL_DIR}"
    fi

    if [[ "$(id -u)" -ne 0 && ! -w "${INSTALL_DIR}" && -n "$(command -v sudo || true)" ]]; then
        sudo chown "$(id -u):$(id -g)" "${INSTALL_DIR}"
    fi

    [[ -w "${INSTALL_DIR}" ]] || die "目录不可写：${INSTALL_DIR}"
}

write_compose_file() {
    cat > "${COMPOSE_FILE}" <<'EOF_COMPOSE'
services:
  xiaoyuposthub:
    image: ${XIAOYUPOSTHUB_IMAGE:-pylintech/xiaoyuposthub:latest}
    container_name: XiaoyuPostHub
    restart: unless-stopped

    ports:
      - "${XIAOYUPOSTHUB_PORT:-8080}:8080"

    env_file:
      - .env

    volumes:
      - xiaoyuposthub_data:/data

    security_opt:
      - no-new-privileges:true

    networks:
      - xiaoyuposthub_network

volumes:
  xiaoyuposthub_data:
    name: xiaoyuposthub_data

networks:
  xiaoyuposthub_network:
    name: ${XIAOYUPOSTHUB_NETWORK:-xiaoyuposthub-network}
    external: ${XIAOYUPOSTHUB_NETWORK_EXTERNAL:-false}
EOF_COMPOSE
}

# prompt_default 带默认值交互读取；空输入时使用默认值。
prompt_default() {
    local label="$1"
    local default_value="$2"
    local value=""

    printf "%s [%s]: " "${label}" "${default_value}" >&2
    IFS= read -r value
    printf "%s" "${value:-${default_value}}"
}

prompt_secret_required() {
    local name="$1" label="$2" value=""
    while [[ -z "${value}" ]]; do
        printf "%s（输入不回显）: " "${label}" >&2
        IFS= read -r -s value
        printf "\n" >&2
        [[ -n "${value}" ]] || warn "${name} 不能为空" >&2
    done
    printf '%s' "${value}"
}

# ensure_env_file 创建空配置骨架；随后由配置检查逐项补全。
ensure_env_file() {
    [[ -f "${ENV_FILE}" ]] && return 0
    [[ -t 0 ]] || die "未找到 .env，且当前不是交互终端"
    warn "首次运行，需要创建配置文件"
    cat > "${ENV_FILE}" <<'EOF_ENV'
# XiaoyuPostHub 配置

# 数据库
DATABASE_URL=

# 超级管理员
SUPER_ADMIN_USERNAME=
SUPER_ADMIN_PASSWORD_HASH=

# Docker
XIAOYUPOSTHUB_IMAGE=pylintech/xiaoyuposthub:latest
XIAOYUPOSTHUB_PORT=
XIAOYUPOSTHUB_NETWORK=
XIAOYUPOSTHUB_NETWORK_EXTERNAL=

# 前端
STATIC_DIR=/app/web

# HTTPS 使用 true，直接 HTTP 使用 false
SESSION_COOKIE_SECURE=true
EOF_ENV
    chmod 600 "${ENV_FILE}" || true
}

# remove_conflicting_container 删除旧容器（不在本 compose 项目下的同名容器）。
remove_conflicting_container() {
    local id=""
    local project=""

    id="$(docker ps -aq --filter "name=^/${CONTAINER_NAME}$" | head -n 1 || true)"
    [[ -n "${id}" ]] || return 0

    project="$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project" }}' "${id}" 2>/dev/null || true)"
    [[ "${project}" == "${PROJECT_NAME}" ]] && return 0

    warn "发现同名旧容器，将重新创建"
    docker rm -f "${CONTAINER_NAME}" >/dev/null
}

read_env_value() {
    local key="$1"
    local line=""
    [[ -f "${ENV_FILE}" ]] || return 0
    line="$(grep -E "^${key}=" "${ENV_FILE}" | tail -n 1 || true)"
    [[ -n "${line}" ]] || return 0
    line="${line#*=}"
    line="${line%\'}"; line="${line#\'}"
    line="${line%\"}"; line="${line#\"}"
    printf "%s" "${line}"
}

write_env_value() {
    local key="$1"
    local value="$2"
    local tmp=""
    [[ -f "${ENV_FILE}" ]] || return 0
    tmp="$(mktemp "${ENV_FILE}.XXXXXX")"
    awk -v key="${key}" -v value="${value}" '
        BEGIN { quote=sprintf("%c", 39) }
        index($0, key "=") == 1 { print key "=" quote value quote; found=1; next }
        { print }
        END { if (!found) print key "=" quote value quote }
    ' "${ENV_FILE}" > "${tmp}"
    chmod 600 "${tmp}"
    mv "${tmp}" "${ENV_FILE}"
}

require_interactive_fix() {
    local key="$1"
    [[ -t 0 ]] || die "配置 ${key} 缺失或无效，且当前不是交互终端"
}

is_valid_database_url() {
    [[ "$1" == postgres://* || "$1" == postgresql://* ]] \
        && [[ "$1" != *[[:space:]]* && "$1" != *"'"* ]]
}

is_valid_bcrypt_hash() {
    local value="$1"
    local pattern='^\$2[aby]\$12\$[./A-Za-z0-9]{53}$'
    [[ "${value}" =~ ${pattern} ]]
}

is_valid_port() {
    [[ "$1" =~ ^[0-9]+$ ]] && (( 10#$1 >= 1 && 10#$1 <= 65535 ))
}

is_valid_network_name() {
    [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]]
}

ensure_host_port() {
    local value=""
    value="${XIAOYUPOSTHUB_PORT:-$(read_env_value XIAOYUPOSTHUB_PORT)}"
    if is_valid_port "${value}"; then
        write_env_value XIAOYUPOSTHUB_PORT "${value}"
        return 0
    fi
    [[ -n "${value}" ]] && warn "XIAOYUPOSTHUB_PORT 必须是 1-65535"
    require_interactive_fix XIAOYUPOSTHUB_PORT
    while true; do
        value="$(prompt_default "对外暴露端口" "${PORT_DEFAULT}")"
        is_valid_port "${value}" && break
        warn "端口必须是 1-65535"
    done
    write_env_value XIAOYUPOSTHUB_PORT "${value}"
}

ensure_fixed_defaults() {
    local value="" normalized=""
    value="$(read_env_value STATIC_DIR)"
    [[ -n "${value}" ]] || write_env_value STATIC_DIR /app/web

    value="$(read_env_value SESSION_COOKIE_SECURE)"
    normalized="$(printf '%s' "${value}" | tr '[:upper:]' '[:lower:]')"
    case "${normalized}" in
        true|false) ;;
        "") write_env_value SESSION_COOKIE_SECURE true ;;
        *)
            warn "SESSION_COOKIE_SECURE 只能是 true 或 false"
            require_interactive_fix SESSION_COOKIE_SECURE
            while true; do
                value="$(prompt_default "是否仅允许 HTTPS Cookie（true/false）" "true")"
                normalized="$(printf '%s' "${value}" | tr '[:upper:]' '[:lower:]')"
                case "${normalized}" in
                    true|false) write_env_value SESSION_COOKIE_SECURE "${normalized}"; break ;;
                    *) warn "请输入 true 或 false" ;;
                esac
            done
            ;;
    esac
}

select_network() {
    local -a networks=()
    local choice="" name="" index=1
    while IFS= read -r name; do
        [[ -n "${name}" ]] && networks[${#networks[@]}]="${name}"
    done < <(docker network ls --filter driver=bridge --format '{{.Name}}' | sort)

    info "请选择 XiaoyuPostHub 要加入的 Docker 网络：" >&2
    for name in "${networks[@]}"; do
        printf "  %d) 使用已有网络 %s\n" "${index}" "${name}" >&2
        ((index += 1))
    done
    printf "  %d) 新建网络\n" "${index}" >&2

    while true; do
        printf "请输入数字: " >&2
        IFS= read -r choice
        if [[ "${choice}" =~ ^[0-9]+$ ]] && (( choice >= 1 && choice <= index )); then
            break
        fi
        warn "请输入 1-${index} 之间的数字" >&2
    done

    if (( choice <= ${#networks[@]} )); then
        SELECTED_NETWORK="${networks[choice - 1]}"
        SELECTED_NETWORK_EXTERNAL=true
        return 0
    fi

    while true; do
        name="$(prompt_default "新网络名称" "${NETWORK_DEFAULT}")"
        if ! is_valid_network_name "${name}"; then
            warn "网络名称只能包含字母、数字、点、下划线和连字符" >&2
            continue
        fi
        if docker network inspect "${name}" >/dev/null 2>&1; then
            warn "网络 ${name} 已存在，请从已有网络列表选择或换一个名称" >&2
            continue
        fi
        SELECTED_NETWORK="${name}"
        SELECTED_NETWORK_EXTERNAL=false
        return 0
    done
}

ensure_network() {
    local name="${XIAOYUPOSTHUB_NETWORK:-}"
    local external="${XIAOYUPOSTHUB_NETWORK_EXTERNAL:-}"
    local configured_name configured_external normalized_external managed_project=""
    configured_name="$(read_env_value XIAOYUPOSTHUB_NETWORK)"
    configured_external="$(read_env_value XIAOYUPOSTHUB_NETWORK_EXTERNAL)"
    name="${name:-${configured_name}}"
    external="${external:-${configured_external}}"
    normalized_external="$(printf '%s' "${external}" | tr '[:upper:]' '[:lower:]')"

    if [[ -n "${name}" && -z "${normalized_external}" ]] && is_valid_network_name "${name}"; then
        if docker network inspect "${name}" >/dev/null 2>&1; then
            normalized_external=true
        else
            normalized_external=false
        fi
    fi

    if [[ -n "${name}" ]] && is_valid_network_name "${name}"; then
        case "${normalized_external}" in
            true)
                if docker network inspect "${name}" >/dev/null 2>&1; then
                    write_env_value XIAOYUPOSTHUB_NETWORK "${name}"
                    write_env_value XIAOYUPOSTHUB_NETWORK_EXTERNAL true
                    return 0
                fi
                warn "配置的外部网络 ${name} 不存在"
                ;;
            false)
                if docker network inspect "${name}" >/dev/null 2>&1; then
                    managed_project="$(docker network inspect -f '{{index .Labels "com.docker.compose.project"}}' "${name}" 2>/dev/null || true)"
                    if [[ "${managed_project}" == "${PROJECT_NAME}" ]]; then
                        # Compose 创建的网络在重复安装时已经存在，仍由 Compose 管理。
                        write_env_value XIAOYUPOSTHUB_NETWORK "${name}"
                        write_env_value XIAOYUPOSTHUB_NETWORK_EXTERNAL false
                        return 0
                    fi
                    warn "网络 ${name} 已存在但不归当前 Compose 项目管理"
                else
                    write_env_value XIAOYUPOSTHUB_NETWORK "${name}"
                    write_env_value XIAOYUPOSTHUB_NETWORK_EXTERNAL false
                    return 0
                fi
                ;;
            *) warn "XIAOYUPOSTHUB_NETWORK_EXTERNAL 必须是 true 或 false" ;;
        esac
    elif [[ -n "${name}" ]]; then
        warn "Docker 网络名称 ${name} 无效"
    fi

    require_interactive_fix XIAOYUPOSTHUB_NETWORK
    SELECTED_NETWORK=""
    SELECTED_NETWORK_EXTERNAL=""
    select_network
    write_env_value XIAOYUPOSTHUB_NETWORK "${SELECTED_NETWORK}"
    write_env_value XIAOYUPOSTHUB_NETWORK_EXTERNAL "${SELECTED_NETWORK_EXTERNAL}"
}

prepare_selected_network() {
    local name external
    name="$(read_env_value XIAOYUPOSTHUB_NETWORK)"
    external="$(read_env_value XIAOYUPOSTHUB_NETWORK_EXTERNAL)"

    if docker network inspect "${name}" >/dev/null 2>&1; then
        return 0
    fi
    [[ "${external}" == "false" ]] || die "Docker 网络不存在：${name}"

    run_step "创建 Docker 网络 ${name}" docker network create "${name}"
    # 网络已由安装脚本创建，交给 Compose 作为外部网络复用。
    write_env_value XIAOYUPOSTHUB_NETWORK_EXTERNAL true
}

show_database_error() {
    local log="$1"
    warn "数据库连接或权限检查失败：" >&2
    sed 's/^/    /' "${log}" >&2
}

test_database_url_in_container() {
    local image="$1" network="$2" database_url="$3" log=""
    log="$(mktemp)"
    if DATABASE_URL="${database_url}" docker run --rm \
        --network "${network}" \
        -e XPH_INTERNAL_DATABASE_ACTION=test \
        -e DATABASE_URL \
        --entrypoint /app/xph-backend \
        "${image}" >"${log}" 2>&1; then
        rm -f "${log}"
        return 0
    fi
    show_database_error "${log}"
    rm -f "${log}"
    return 1
}

provision_database_in_container() {
    local image="$1" network="$2" admin_url="$3" log="" output=""
    log="$(mktemp)"
    if output="$(XPH_DATABASE_ADMIN_URL="${admin_url}" docker run --rm \
        --network "${network}" \
        -e XPH_INTERNAL_DATABASE_ACTION=provision \
        -e XPH_DATABASE_ADMIN_URL \
        --entrypoint /app/xph-backend \
        "${image}" 2>"${log}")" && is_valid_database_url "${output}"; then
        rm -f "${log}"
        printf '%s' "${output}"
        return 0
    fi
    show_database_error "${log}"
    rm -f "${log}"
    return 1
}

prompt_database_url() {
    local image="$1" network="$2" current="" choice="" value=""
    current="$(read_env_value DATABASE_URL)"
    if is_valid_database_url "${current}"; then
        info "检查现有数据库配置"
        if test_database_url_in_container "${image}" "${network}" "${current}"; then
            ok "数据库连接和建表权限正常"
            return 0
        fi
        warn "现有数据库配置不可用，请重新配置"
    fi

    require_interactive_fix DATABASE_URL
    while true; do
        info "请选择数据库配置方式：" >&2
        printf "  1) 自动创建数据库（输入管理员连接）\n" >&2
        printf "  2) 使用现有数据库（输入应用连接）\n" >&2
        printf "请输入数字: " >&2
        IFS= read -r choice
        case "${choice}" in
            1)
                value="$(prompt_secret_required DATABASE_URL "PostgreSQL 管理员连接")"
                if ! is_valid_database_url "${value}"; then
                    warn "连接地址格式无效" >&2
                    continue
                fi
                if value="$(provision_database_in_container "${image}" "${network}" "${value}")"; then
                    write_env_value DATABASE_URL "${value}"
                    ok "已创建并验证专用数据库"
                    return 0
                fi
                ;;
            2)
                value="$(prompt_secret_required DATABASE_URL "应用数据库连接")"
                if ! is_valid_database_url "${value}"; then
                    warn "连接地址格式无效" >&2
                    continue
                fi
                if test_database_url_in_container "${image}" "${network}" "${value}"; then
                    write_env_value DATABASE_URL "${value}"
                    ok "数据库连接和建表权限正常"
                    return 0
                fi
                ;;
            *) warn "请输入 1 或 2" >&2 ;;
        esac
        warn "请重新输入数据库配置" >&2
    done
}

configure_super_admin() {
    local image="$1" username="" hash="" first="" second=""
    username="$(read_env_value SUPER_ADMIN_USERNAME)"
    hash="$(read_env_value SUPER_ADMIN_PASSWORD_HASH)"
    if [[ -n "${username//[[:space:]]/}" && "${username}" != *"'"* ]] && is_valid_bcrypt_hash "${hash}"; then
        return 0
    fi

    require_interactive_fix SUPER_ADMIN_USERNAME
    while true; do
        username="$(prompt_default "站点管理员账号" "${username:-admin}")"
        [[ -n "${username//[[:space:]]/}" && "${username}" != *"'"* ]] && break
        warn "管理员账号不能为空且不能包含单引号" >&2
    done
    while true; do
        printf "站点管理员密码: " >&2; IFS= read -r -s first; printf "\n" >&2
        [[ -n "${first}" ]] || { warn "密码不能为空" >&2; continue; }
        printf "确认管理员密码: " >&2; IFS= read -r -s second; printf "\n" >&2
        [[ "${first}" == "${second}" ]] || { warn "两次密码不一致" >&2; continue; }
        hash="$(printf %s "${first}" | docker run --rm -i \
            -e XPH_INTERNAL_HASH_PASSWORD=true \
            --entrypoint /app/xph-backend "${image}")" || die "生成管理员密码哈希失败"
        is_valid_bcrypt_hash "${hash}" || die "生成管理员密码哈希失败"
        break
    done

    write_env_value SUPER_ADMIN_USERNAME "${username}"
    write_env_value SUPER_ADMIN_PASSWORD_HASH "${hash}"
    DISPLAY_ADMIN_PASSWORD="${first}"
    unset first second
}

print_install_summary() {
    local image="$1" username network password_display
    username="$(read_env_value SUPER_ADMIN_USERNAME)"
    network="$(read_env_value XIAOYUPOSTHUB_NETWORK)"
    password_display="${DISPLAY_ADMIN_PASSWORD:-保持原密码（脚本不保存明文）}"

    printf "\n安装完成\n"
    printf "  访问地址：http://localhost:%s\n" "$(current_port)"
    printf "  管理员账号：%s\n" "${username}"
    printf "  管理员密码：%s\n" "${password_display}"
    printf "  Docker 网络：%s\n" "${network}"
    printf "  Docker 镜像：%s\n" "${image}"
    printf "  配置文件：%s\n" "${ENV_FILE}"
    [[ -z "${DISPLAY_ADMIN_PASSWORD}" ]] || warn "管理员密码仅显示本次，请立即保存"
    warn "提示：使用反向代理时，请传递 X-Real-IP 请求头，以便后端记录真实客户端 IP"
}

resolve_image() {
    local configured="" value=""
    configured="$(read_env_value XIAOYUPOSTHUB_IMAGE)"
    value="${XIAOYUPOSTHUB_IMAGE:-${configured:-${IMAGE_DEFAULT}}}"
    if [[ -n "${value}" && "${value}" != *[[:space:]]* && "${value}" != *"'"* ]]; then
        printf '%s' "${value}"
        return 0
    fi

    [[ -n "${value}" ]] && warn "XIAOYUPOSTHUB_IMAGE 无效" >&2
    require_interactive_fix XIAOYUPOSTHUB_IMAGE
    while true; do
        value="$(prompt_default "Docker 镜像" "${IMAGE_DEFAULT}")"
        if [[ -n "${value}" && "${value}" != *[[:space:]]* && "${value}" != *"'"* ]]; then
            printf '%s' "${value}"
            return 0
        fi
        warn "Docker 镜像名称不能为空或包含空白、单引号" >&2
    done
}

current_port() {
    local port=""
    port="$(read_env_value XIAOYUPOSTHUB_PORT)"
    printf "%s" "${port:-${PORT_DEFAULT}}"
}

install_or_update() {
    local image=""
    local network=""

    check_docker
    prepare_install_dir
    write_compose_file
    ensure_env_file

    image="$(resolve_image)"
    export XIAOYUPOSTHUB_IMAGE="${image}"
    write_env_value XIAOYUPOSTHUB_IMAGE "${image}"
    run_step "拉取镜像 ${image}" docker pull "${image}"
    run_step "从目标镜像提取数据库迁移助手" extract_migration_assistant "${image}"
    run_step "拉取数据库迁移客户端 ${MIGRATION_POSTGRES_IMAGE}" docker pull "${MIGRATION_POSTGRES_IMAGE}"

    ensure_network
    prepare_selected_network
    network="$(read_env_value XIAOYUPOSTHUB_NETWORK)"
    export XIAOYUPOSTHUB_NETWORK="${network}"
    export XIAOYUPOSTHUB_NETWORK_EXTERNAL="$(read_env_value XIAOYUPOSTHUB_NETWORK_EXTERNAL)"

    prompt_database_url "${image}" "${network}"
    ensure_host_port
    ensure_fixed_defaults
    export XIAOYUPOSTHUB_PORT="$(read_env_value XIAOYUPOSTHUB_PORT)"
    configure_super_admin "${image}"
    chmod 600 "${ENV_FILE}" || true
    remove_conflicting_container

    if docker inspect "${CONTAINER_NAME}" >/dev/null 2>&1; then
        run_step "停止旧服务" docker stop "${CONTAINER_NAME}"
    fi
    run_step "按顺序迁移数据库" env \
        DATABASE_URL="$(read_env_value DATABASE_URL)" \
        XIAOYUPOSTHUB_IMAGE="${image}" \
        XIAOYUPOSTHUB_NETWORK="${network}" \
        XPH_MIGRATION_POSTGRES_IMAGE="${MIGRATION_POSTGRES_IMAGE}" \
        bash "${MIGRATION_ASSISTANT_FILE}"

    run_step "检查 Docker Compose 配置" compose config --quiet
    run_step "更新并启动服务" compose up -d --force-recreate --remove-orphans
    print_install_summary "${image}"
}

show_logs() {
    check_docker
    require_compose_file
    compose logs -f
}

show_status() {
    check_docker
    require_compose_file
    local status=""
    status="$(docker inspect -f '{{.State.Status}}' "${CONTAINER_NAME}" 2>/dev/null || true)"
    [[ -n "${status}" ]] || die "容器不存在：${CONTAINER_NAME}"
    printf "容器：%s\n状态：%s\n端口：%s\n网络：%s\n" \
        "${CONTAINER_NAME}" "${status}" "$(current_port)" "$(read_env_value XIAOYUPOSTHUB_NETWORK)"
}

stop_service() {
    check_docker
    require_compose_file
    run_step "停止服务" compose down --remove-orphans
}

restart_service() {
    check_docker
    require_compose_file
    run_step "重启服务" compose restart
}

uninstall_service() {
    check_docker

    if [[ -f "${COMPOSE_FILE}" ]]; then
        if [[ "${PURGE}" == true ]]; then
            run_step "停止服务并删除数据卷" compose down --remove-orphans --volumes
        else
            run_step "停止服务" compose down --remove-orphans
        fi
    else
        docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
    fi

    if [[ -d "${INSTALL_DIR}" ]]; then
        run_step "删除安装目录" run_privileged rm -rf "${INSTALL_DIR}"
    fi

    ok "完成：卸载"
}

main() {
    parse_args "$@"

    case "${ACTION}" in
        install) install_or_update ;;
        logs) show_logs ;;
        status) show_status ;;
        stop) stop_service ;;
        restart) restart_service ;;
        uninstall) uninstall_service ;;
        *) die "未知操作：${ACTION}" ;;
    esac
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
