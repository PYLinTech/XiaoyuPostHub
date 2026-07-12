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

set -Eeuo pipefail

APP_NAME="XiaoyuPostHub"
PROJECT_NAME="xiaoyuposthub"
CONTAINER_NAME="XiaoyuPostHub"
IMAGE_DEFAULT="pylintech/xiaoyuposthub:latest"
PORT_DEFAULT="8080"

INSTALL_DIR="${XIAOYUPOSTHUB_HOME:-/opt/xiaoyuposthub}"
COMPOSE_FILE="${INSTALL_DIR}/compose.yaml"
ENV_FILE="${INSTALL_DIR}/.env"

ACTION="install"
ACTION_SET=false
PURGE=false

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

volumes:
  xiaoyuposthub_data:
    name: xiaoyuposthub_data
EOF_COMPOSE
}

# env_quote 使用单引号字面量，避免 bcrypt 中的 $ 被 Compose 插值。
env_quote() {
    local value="$1"
    [[ "${value}" != *"'"* ]] || die ".env 配置值不能包含单引号"
    printf "'%s'" "${value}"
}

# prompt_required 通用必填项交互读取。
prompt_required() {
    local name="$1"
    local label="$2"
    local value=""

    while [[ -z "${value}" ]]; do
        printf "%s: " "${label}" >&2
        IFS= read -r value
        [[ -n "${value}" ]] || warn "${name} 不能为空" >&2
    done

    printf "%s" "${value}"
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

prompt_bcrypt_hash() {
    local image="$1" first="" second="" hash=""
    while true; do
        printf "管理员密码: " >&2; IFS= read -r -s first; printf "\n" >&2
        [[ -n "${first}" ]] || { warn "密码不能为空" >&2; continue; }
        printf "确认密码: " >&2; IFS= read -r -s second; printf "\n" >&2
        [[ "${first}" == "${second}" ]] || { warn "两次密码不一致" >&2; continue; }
        hash="$(printf %s "${first}" | docker run --rm -i -e XPH_INTERNAL_HASH_PASSWORD=true --entrypoint /app/xph-backend "${image}")" || die "生成 bcrypt 哈希失败"
        unset first second
        [[ -n "${hash}" ]] || die "生成 bcrypt 哈希失败"
        printf "%s" "${hash}"; return 0
    done
}

# create_env_file 首次运行时生成部署配置。
#
# 配置值统一使用单引号字面量。
create_env_file() {
    [[ -f "${ENV_FILE}" ]] && return 0
    [[ -t 0 ]] || die "未找到 .env，且当前不是交互终端"

    warn "首次运行，需要创建配置文件"

    local database_url
    local admin_username
    local admin_password_hash
    local host_port
    local image_name

    database_url="$(prompt_required "DATABASE_URL" "数据库地址 DATABASE_URL")"
    admin_username="$(prompt_default "管理员账号" "admin")"
    image_name="${XIAOYUPOSTHUB_IMAGE:-${IMAGE_DEFAULT}}"
    admin_password_hash="$(prompt_bcrypt_hash "${image_name}")"
    host_port="$(prompt_default "对外暴露端口" "${PORT_DEFAULT}")"

    cat > "${ENV_FILE}" <<EOF_ENV
# XiaoyuPostHub 配置

# 数据库
DATABASE_URL=$(env_quote "${database_url}")

# 超级管理员
SUPER_ADMIN_USERNAME=$(env_quote "${admin_username}")
SUPER_ADMIN_PASSWORD_HASH=$(env_quote "${admin_password_hash}")

# Docker
XIAOYUPOSTHUB_IMAGE=$(env_quote "${image_name}")
XIAOYUPOSTHUB_PORT=$(env_quote "${host_port}")

# 前端
STATIC_DIR=/app/web

# HTTPS 使用 true，直接 HTTP 使用 false
SESSION_COOKIE_SECURE=true
EOF_ENV

    chmod 600 "${ENV_FILE}" || true
    ok "配置已创建：${ENV_FILE}"
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

current_port() {
    local port=""
    port="$(read_env_value XIAOYUPOSTHUB_PORT)"
    printf "%s" "${port:-${PORT_DEFAULT}}"
}

install_or_update() {
    local configured_image=""
    local image=""

    check_docker
    prepare_install_dir
    write_compose_file
    configured_image="$(read_env_value XIAOYUPOSTHUB_IMAGE)"
    image="${XIAOYUPOSTHUB_IMAGE:-${configured_image:-${IMAGE_DEFAULT}}}"
    export XIAOYUPOSTHUB_IMAGE="${image}"
    run_step "拉取镜像 ${image}" docker pull "${image}"
    create_env_file
    write_env_value XIAOYUPOSTHUB_IMAGE "${image}"
    remove_conflicting_container

    run_step "启动服务" compose up -d --remove-orphans

    ok "完成：${APP_NAME} 已启动（http://localhost:$(current_port)）"
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
    printf "容器：%s\n状态：%s\n端口：%s\n" "${CONTAINER_NAME}" "${status}" "$(current_port)"
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

main "$@"
