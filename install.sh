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
#   ./install.sh --uninstall --purge 卸载，并删除数据卷
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
DATA_VOLUME="xiaoyuposthub_data"

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
  ./install.sh --stop              停止服务，保留数据
  ./install.sh --restart           重启服务
  ./install.sh --uninstall         卸载，保留数据卷
  ./install.sh --uninstall --purge 卸载，并删除数据卷
  ./install.sh -h|--help           查看帮助

环境变量：
  XIAOYUPOSTHUB_HOME               安装目录，默认 ${INSTALL_DIR}
  XIAOYUPOSTHUB_IMAGE              镜像，默认 ${IMAGE_DEFAULT}

默认值：
  访问端口：${PORT_DEFAULT}
  配置文件：${ENV_FILE}
  数据卷：${DATA_VOLUME}
EOF_USAGE
}

info() { printf "%b%s%b\n" "${BLUE}" "$*" "${RESET}"; }
ok() { printf "%b%s%b\n" "${GREEN}" "$*" "${RESET}"; }
warn() { printf "%b%s%b\n" "${YELLOW}" "$*" "${RESET}"; }
die() {
    printf "%b错误：%s%b\n" "${RED}" "$*" "${RESET}" >&2
    exit 1
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
        info "创建目录：${INSTALL_DIR}"
        run_privileged mkdir -p "${INSTALL_DIR}"
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
    pull_policy: always
    container_name: XiaoyuPostHub
    restart: unless-stopped

    ports:
      - "${XIAOYUPOSTHUB_PORT:-8080}:8080"

    env_file:
      - .env

    environment:
      DATABASE_URL: ${DATABASE_URL:?请在 .env 中配置 DATABASE_URL}
      SUPER_ADMIN_USERNAME: ${SUPER_ADMIN_USERNAME:?请在 .env 中配置 SUPER_ADMIN_USERNAME}
      SUPER_ADMIN_PASSWORD_HASH: ${SUPER_ADMIN_PASSWORD_HASH:?请在 .env 中配置 SUPER_ADMIN_PASSWORD_HASH}
      DATA_DIR: /data

    volumes:
      - xiaoyuposthub_data:/data

    security_opt:
      - no-new-privileges:true

volumes:
  xiaoyuposthub_data:
    name: xiaoyuposthub_data
    external: true
EOF_COMPOSE
}

env_value() {
    local value="$1"

    if [[ "${value}" == *$'\n'* || "${value}" == *$'\r'* ]]; then
        die ".env 配置值不能包含换行"
    fi

    printf "%s" "${value}"
}

hash_password() {
    local password="$1"
    local salt=""
    local hash=""

    if has_cmd openssl; then
        salt="$(openssl rand -hex 16)"
        hash="$(printf "%s:%s" "${salt}" "${password}" | openssl dgst -sha256 -binary | od -An -vtx1 | tr -d ' \n')"
    elif [[ -r /dev/urandom ]] && has_cmd od && has_cmd sha256sum; then
        salt="$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
        hash="$(printf "%s:%s" "${salt}" "${password}" | sha256sum | awk '{print $1}')"
    else
        die "无法生成密码哈希，请安装 openssl 或 sha256sum"
    fi

    printf "sha256:%s:%s" "${salt}" "${hash}"
}

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

prompt_default() {
    local label="$1"
    local default_value="$2"
    local value=""

    printf "%s [%s]: " "${label}" "${default_value}" >&2
    IFS= read -r value
    printf "%s" "${value:-${default_value}}"
}

prompt_password() {
    local first=""
    local second=""

    while true; do
        printf "管理员密码: " >&2
        IFS= read -r -s first
        printf "\n" >&2

        [[ -n "${first}" ]] || { warn "密码不能为空" >&2; continue; }

        printf "确认密码: " >&2
        IFS= read -r -s second
        printf "\n" >&2

        [[ "${first}" == "${second}" ]] || { warn "两次密码不一致" >&2; continue; }

        printf "%s" "${first}"
        return 0
    done
}

create_env_file() {
    [[ -f "${ENV_FILE}" ]] && return 0
    [[ -t 0 ]] || die "未找到 .env，且当前不是交互终端"

    warn "首次运行，需要创建配置文件"

    local database_url
    local admin_username
    local admin_password
    local admin_password_hash
    local port
    local image

    database_url="$(prompt_required "DATABASE_URL" "数据库地址 DATABASE_URL")"
    admin_username="$(prompt_default "管理员账号" "admin")"
    admin_password="$(prompt_password)"
    admin_password_hash="$(hash_password "${admin_password}")"
    unset admin_password
    port="$(prompt_default "访问端口" "${PORT_DEFAULT}")"
    image="${XIAOYUPOSTHUB_IMAGE:-${IMAGE_DEFAULT}}"

    cat > "${ENV_FILE}" <<EOF_ENV
# XiaoyuPostHub 运行配置

# 数据库连接地址：包含协议、用户、密码、地址、端口、数据库名等完整信息。
# PostgreSQL 示例：postgres://user:password@host:5432/database?sslmode=disable
# MySQL 示例：user:password@tcp(host:3306)/database?charset=utf8mb4&parseTime=True&loc=Local
DATABASE_URL=$(env_value "${database_url}")

# 超级管理员账号
SUPER_ADMIN_USERNAME=$(env_value "${admin_username}")

# 超级管理员密码
# 格式：sha256:<salt>:<hash>
# 推荐使用 install.sh 首次运行时交互生成；不要手写明文密码。
SUPER_ADMIN_PASSWORD_HASH=$(env_value "${admin_password_hash}")

# 可选：手动指定使用的镜像
XIAOYUPOSTHUB_IMAGE=$(env_value "${image}")

# 可选：指定宿主机访问端口
XIAOYUPOSTHUB_PORT=$(env_value "${port}")
EOF_ENV

    chmod 600 "${ENV_FILE}" || true
    ok "配置已创建：${ENV_FILE}"
}

prepare_volume() {
    if ! docker volume inspect "${DATA_VOLUME}" >/dev/null 2>&1; then
        info "创建数据卷：${DATA_VOLUME}"
        docker volume create "${DATA_VOLUME}" >/dev/null
    fi
}

remove_conflicting_container() {
    local id=""
    local project=""

    id="$(docker ps -aq --filter "name=^/${CONTAINER_NAME}$" | head -n 1 || true)"
    [[ -n "${id}" ]] || return 0

    project="$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project" }}' "${id}" 2>/dev/null || true)"
    [[ "${project}" == "${PROJECT_NAME}" ]] && return 0

    warn "发现同名旧容器，将重新创建，数据保留"
    docker rm -f "${CONTAINER_NAME}" >/dev/null
}

current_port() {
    local line=""

    if [[ -f "${ENV_FILE}" ]]; then
        line="$(grep -E '^XIAOYUPOSTHUB_PORT=' "${ENV_FILE}" | tail -n 1 || true)"
        if [[ -n "${line}" ]]; then
            line="${line#XIAOYUPOSTHUB_PORT=}"
            line="${line%\'}"; line="${line#\'}"
            line="${line%\"}"; line="${line#\"}"
            printf "%s" "${line}"
            return 0
        fi
    fi

    printf "%s" "${PORT_DEFAULT}"
}

install_or_update() {
    local image="${XIAOYUPOSTHUB_IMAGE:-${IMAGE_DEFAULT}}"

    check_docker
    prepare_install_dir
    write_compose_file
    create_env_file
    prepare_volume
    remove_conflicting_container

    info "拉取镜像：${image}"
    compose pull

    info "启动服务"
    compose up -d --remove-orphans

    ok "${APP_NAME} 已启动"
    printf "访问地址：http://localhost:%s\n" "$(current_port)"
    printf "安装目录：%s\n" "${INSTALL_DIR}"
    printf "配置文件：%s\n" "${ENV_FILE}"
    printf "数据卷：%s\n" "${DATA_VOLUME}"
}

show_logs() {
    check_docker
    require_compose_file
    compose logs -f
}

show_status() {
    check_docker
    require_compose_file
    compose ps
}

stop_service() {
    check_docker
    require_compose_file
    compose down --remove-orphans
    ok "服务已停止，数据已保留"
}

restart_service() {
    check_docker
    require_compose_file
    compose restart
    ok "服务已重启"
}

uninstall_service() {
    check_docker

    if [[ -f "${COMPOSE_FILE}" ]]; then
        compose down --remove-orphans || true
    else
        docker rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
    fi

    if [[ "${PURGE}" == true ]]; then
        warn "删除数据卷：${DATA_VOLUME}"
        docker volume rm "${DATA_VOLUME}" >/dev/null 2>&1 || true
    else
        warn "数据卷已保留：${DATA_VOLUME}"
    fi

    if [[ -d "${INSTALL_DIR}" ]]; then
        info "删除目录：${INSTALL_DIR}"
        run_privileged rm -rf "${INSTALL_DIR}"
    fi

    ok "卸载完成"
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
