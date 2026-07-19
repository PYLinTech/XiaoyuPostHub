#!/usr/bin/env bash
# build.sh — XiaoyuPostHub 项目总构建器
#
# 作用：
#   构建完整项目，整理运行产物，并分别生成 linux/amd64 与 linux/arm64 Docker 镜像文件。
#
# 流程：
#   1. 构建前端
#   2. 整理前端产物到 deploy/app/web
#   3. 构建 linux/amd64 后端与 Docker 镜像
#   4. 导出 linux/amd64 镜像文件
#   5. 构建 linux/arm64 后端与 Docker 镜像
#   6. 导出 linux/arm64 镜像文件
#
# 用法：
#   ./build.sh                              # 交互输入版本号
#   ./build.sh --version v1.0.0
#   ./build.sh --version v1.0.0 --image pylintech/xiaoyuposthub
#   ./build.sh --version v1.0.0 --no-cache
#
# 说明：
#   固定构建两个容器架构：linux/amd64、linux/arm64。
#   镜像文件会输出到 deploy/images/。

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
FRONTEND_DIR="${ROOT_DIR}/frontend"
BACKEND_DIR="${ROOT_DIR}/backend"
MIGRATIONS_DIR="${ROOT_DIR}/migrations"
MIGRATION_ASSISTANT_SOURCE="${ROOT_DIR}/migration-assistant.sh"
DEPLOY_DIR="${ROOT_DIR}/deploy"
APP_DIR="${DEPLOY_DIR}/app"
IMAGE_DIR="${DEPLOY_DIR}/images"
LOG_DIR="${ROOT_DIR}/.build-logs"

APP_NAME="xiaoyuposthub"
BINARY_NAME="xph-backend"
CONTAINER_OS="linux"
TARGET_ARCHES=("amd64" "arm64")

VERSION=""
IMAGE_NAME="pylintech/xiaoyuposthub"
NO_CACHE=false

CURRENT_STEP=0
TOTAL_STEPS=0
BACKEND_BINARY="${BACKEND_DIR}/bin/${BINARY_NAME}"

BUILT_IMAGES=()
SAVED_FILES=()

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    BLUE=$'\033[34m'
    GREEN=$'\033[32m'
    RED=$'\033[31m'
    RESET=$'\033[0m'
    CLEAR_LINE=$'\r\033[2K'
else
    BLUE=""
    GREEN=""
    RED=""
    RESET=""
    CLEAR_LINE=""
fi

usage() {
    cat <<'EOF_USAGE'
用法：
  ./build.sh
  ./build.sh --version v1.0.0
  ./build.sh --version v1.0.0 --image pylintech/xiaoyuposthub
  ./build.sh --version v1.0.0 --no-cache

参数：
  --version <版本号>       语义版本号，例如 v1.0.1 或 v1.0.0-rc.1
  --image <镜像名>         镜像名，默认 pylintech/xiaoyuposthub
  --no-cache               构建 Docker 镜像时不使用缓存
  -h, --help               查看帮助

说明：
  未传 --version 时，会在交互终端提示输入版本号。
  CI 或其他非交互环境必须显式传入 --version。
  固定构建两个架构：linux/amd64、linux/arm64。
  输出镜像文件：deploy/images/xiaoyuposthub_<version>_linux_<arch>.tar
  参数统一使用空格形式，例如：
    ./build.sh --version v1.0.0
EOF_USAGE
}

fail() {
    printf "%b错误：%s%b\n" "${RED}" "$*" "${RESET}" >&2
    exit 1
}

on_error() {
    local line="$1"
    printf "%b错误：脚本执行失败，位置：第 %s 行%b\n" "${RED}" "${line}" "${RESET}" >&2
    exit 1
}

trap 'on_error "$LINENO"' ERR

command_exists() {
    command -v "$1" >/dev/null 2>&1
}

validate_version() {
    [[ -n "$1" ]] || fail "版本号不能为空"

    if [[ ! "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9][A-Za-z0-9_.-]*)?$ ]]; then
        fail "版本号格式不正确，请使用 v1.0.0 或 v1.0.0-rc.1 这样的格式"
    fi
}

prompt_version_if_missing() {
    [[ -z "${VERSION}" ]] || return 0

    if [[ ! -t 0 ]]; then
        fail "非交互环境未指定版本号，请使用：./build.sh --version v1.0.0"
    fi

    printf "%b请输入构建版本号（例如 v1.0.0）：%b" "${BLUE}" "${RESET}" >&2
    IFS= read -r VERSION || fail "读取版本号失败"
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --version)
                [[ -n "${2:-}" ]] || fail "--version 需要指定版本号，例如：--version v1.0.0"
                VERSION="$2"
                shift 2
                ;;
            --version=*)
                fail "请使用空格形式：--version v1.0.0"
                ;;
            --image)
                [[ -n "${2:-}" ]] || fail "--image 需要指定镜像名，例如：--image pylintech/xiaoyuposthub"
                IMAGE_NAME="$2"
                shift 2
                ;;
            --image=*)
                fail "请使用空格形式：--image pylintech/xiaoyuposthub"
                ;;
            --no-cache)
                NO_CACHE=true
                shift
                ;;
            -h|--help)
                usage
                exit 0
                ;;
            *)
                fail "未知参数：$1"
                ;;
        esac
    done
}

print_step_start() {
    local step_no="$1"
    local step_total="$2"
    local title="$3"

    printf "%b执行：[%d/%d] %s%b\n" "${BLUE}" "${step_no}" "${step_total}" "${title}" "${RESET}"
}

print_step_success() {
    local step_no="$1"
    local step_total="$2"
    local title="$3"
    local detail="${4:-}"

    if [[ -n "${detail}" ]]; then
        printf "%b完成：[%d/%d] %s（%s）%b\n" "${GREEN}" "${step_no}" "${step_total}" "${title}" "${detail}" "${RESET}"
    else
        printf "%b完成：[%d/%d] %s%b\n" "${GREEN}" "${step_no}" "${step_total}" "${title}" "${RESET}"
    fi
}

print_success() {
    local title="$1"
    local detail="${2:-}"

    if [[ -n "${detail}" ]]; then
        printf "%b完成：%s（%s）%b\n" "${GREEN}" "${title}" "${detail}" "${RESET}"
    else
        printf "%b完成：%s%b\n" "${GREEN}" "${title}" "${RESET}"
    fi
}

show_log() {
    local log_file="$1"

    [[ -f "${log_file}" ]] || return 0

    printf "%b错误日志：%s%b\n" "${RED}" "${log_file}" "${RESET}" >&2
    sed 's/^/    /' "${log_file}" >&2
}

run_command() {
    local title="$1"
    local log_name="$2"
    shift 2

    CURRENT_STEP=$((CURRENT_STEP + 1))

    local log_file="${LOG_DIR}/${log_name}.log"

    mkdir -p "${LOG_DIR}"
    rm -f "${log_file}"

    print_step_start "${CURRENT_STEP}" "${TOTAL_STEPS}" "${title}"

    if "$@" >"${log_file}" 2>&1; then
        rm -f "${log_file}"
        print_step_success "${CURRENT_STEP}" "${TOTAL_STEPS}" "${title}"
    else
        show_log "${log_file}"
        fail "${title} 失败"
    fi
}

run_step() {
    local title="$1"
    shift

    CURRENT_STEP=$((CURRENT_STEP + 1))

    print_step_start "${CURRENT_STEP}" "${TOTAL_STEPS}" "${title}"
    "$@"
    print_step_success "${CURRENT_STEP}" "${TOTAL_STEPS}" "${title}"
}

dir_size() {
    du -sh "$1" 2>/dev/null | awk '{print $1}'
}

check_project() {
    command_exists docker || fail "未找到 docker 命令"

    [[ -d "${FRONTEND_DIR}" ]] || fail "未找到前端目录：${FRONTEND_DIR}"
    [[ -d "${BACKEND_DIR}" ]] || fail "未找到后端目录：${BACKEND_DIR}"
    [[ -d "${MIGRATIONS_DIR}" ]] || fail "未找到迁移目录：${MIGRATIONS_DIR}"
    [[ -f "${MIGRATION_ASSISTANT_SOURCE}" ]] || fail "未找到迁移助手：${MIGRATION_ASSISTANT_SOURCE}"
    [[ -d "${DEPLOY_DIR}" ]] || fail "未找到部署目录：${DEPLOY_DIR}"

    [[ -f "${FRONTEND_DIR}/build.sh" ]] || fail "未找到前端构建脚本：${FRONTEND_DIR}/build.sh"
    [[ -f "${BACKEND_DIR}/build.sh" ]] || fail "未找到后端构建脚本：${BACKEND_DIR}/build.sh"
    [[ -f "${DEPLOY_DIR}/Dockerfile" ]] || fail "未找到 Dockerfile：${DEPLOY_DIR}/Dockerfile"
}

prepare_variables() {
    TOTAL_STEPS=$((2 + (${#TARGET_ARCHES[@]} * 4)))
}

prepare_frontend_app() {
    rm -rf "${APP_DIR}"
    mkdir -p "${APP_DIR}"

    mv "${FRONTEND_DIR}/build" "${APP_DIR}/web"
}

prepare_backend_app() {
    cp "${BACKEND_BINARY}" "${APP_DIR}/${BINARY_NAME}"
    chmod +x "${APP_DIR}/${BINARY_NAME}"
    rm -f "${APP_DIR}/migration-assistant.sh"
    cp "${MIGRATION_ASSISTANT_SOURCE}" "${APP_DIR}/migration-assistant.sh"
    chmod 750 "${APP_DIR}/migration-assistant.sh"
    rm -rf "${APP_DIR}/migrations"
    cp -R "${MIGRATIONS_DIR}" "${APP_DIR}/migrations"
}

image_tag_for_arch() {
    local arch="$1"
    printf "%s:%s-%s-%s" "${IMAGE_NAME}" "${VERSION}" "${CONTAINER_OS}" "${arch}"
}

image_file_for_arch() {
    local arch="$1"
    printf "%s_%s_%s_%s.tar" "${APP_NAME}" "${VERSION}" "${CONTAINER_OS}" "${arch}"
}

build_image_for_arch() {
    local arch="$1"
    local platform="${CONTAINER_OS}/${arch}"
    local image_tag
    local -a args

    image_tag="$(image_tag_for_arch "${arch}")"

    args=(
        build
        --platform "${platform}"
        -t "${image_tag}"
        -f "${DEPLOY_DIR}/Dockerfile"
    )

    if ${NO_CACHE}; then
        args+=(--no-cache)
    fi

    args+=("${DEPLOY_DIR}")

    run_command "构建 Docker 镜像 ${platform}" "docker-build-${arch}" docker "${args[@]}"
    BUILT_IMAGES+=("${image_tag}")
}

save_image_for_arch() {
    local arch="$1"
    local image_tag
    local image_file
    local image_file_path

    image_tag="$(image_tag_for_arch "${arch}")"
    image_file="$(image_file_for_arch "${arch}")"
    image_file_path="${IMAGE_DIR}/${image_file}"

    mkdir -p "${IMAGE_DIR}"
    rm -f "${image_file_path}"

    run_command "导出 Docker 镜像 ${CONTAINER_OS}/${arch}" "docker-save-${arch}" docker save -o "${image_file_path}" "${image_tag}"
    SAVED_FILES+=("${image_file_path}")
}

build_arch() {
    local arch="$1"

    run_command "构建后端容器产物 ${CONTAINER_OS}/${arch}" "backend-build-${arch}" \
        bash "${BACKEND_DIR}/build.sh" --os "${CONTAINER_OS}" --arch "${arch}"
    [[ -f "${BACKEND_BINARY}" ]] || fail "未找到后端产物：${BACKEND_BINARY}"

    run_step "整理 deploy/app 后端 ${CONTAINER_OS}/${arch}" prepare_backend_app
    build_image_for_arch "${arch}"
    save_image_for_arch "${arch}"
}

main() {
    parse_args "$@"
    prompt_version_if_missing
    validate_version "${VERSION}"

    check_project
    prepare_variables

    printf "%b构建配置%b\n" "${BLUE}" "${RESET}"
    printf "  容器系统：%s\n" "${CONTAINER_OS}"
    printf "  镜像架构：%s\n" "${TARGET_ARCHES[*]}"
    printf "  版本号：%s\n" "${VERSION}"
    printf "  镜像名：%s\n" "${IMAGE_NAME}"
    printf "  输出目录：%s\n" "${IMAGE_DIR}"

    run_command "构建前端" "frontend-build" bash "${FRONTEND_DIR}/build.sh"
    [[ -d "${FRONTEND_DIR}/build" ]] || fail "未找到前端产物：${FRONTEND_DIR}/build"

    run_step "整理 deploy/app 前端" prepare_frontend_app

    local arch
    for arch in "${TARGET_ARCHES[@]}"; do
        build_arch "${arch}"
    done

    print_success "项目构建完成"

    printf "  镜像：\n"
    local image
    for image in "${BUILT_IMAGES[@]}"; do
        printf "    %s\n" "${image}"
    done

    printf "  镜像文件：\n"
    local file
    for file in "${SAVED_FILES[@]}"; do
        printf "    %s（%s）\n" "${file}" "$(dir_size "${file}")"
    done

    printf "  产物目录：%s\n" "${APP_DIR}"
}

main "$@"
