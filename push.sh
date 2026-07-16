#!/usr/bin/env bash
# push.sh — 交互式推送 XiaoyuPostHub 多架构镜像到 Docker Hub

set -Eeuo pipefail

DEFAULT_IMAGE="pylintech/xiaoyuposthub"
IMAGE_NAME=""
VERSION=""
PUBLISH_LATEST=true
TEMP_DIR=""

if [[ -t 1 && -z "${NO_COLOR:-}" ]]; then
    BLUE=$'\033[34m'
    GREEN=$'\033[32m'
    RED=$'\033[31m'
    RESET=$'\033[0m'
else
    BLUE=""
    GREEN=""
    RED=""
    RESET=""
fi

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

cleanup() {
    if [[ -n "${TEMP_DIR}" && -d "${TEMP_DIR}" ]]; then
        rm -rf "${TEMP_DIR}"
    fi
}

trap cleanup EXIT

command_exists() {
    command -v "$1" >/dev/null 2>&1
}

run_quiet() {
    local title="$1"
    local log_file
    shift

    log_file="${TEMP_DIR}/command.log"
    printf "%b执行：%s%b\n" "${BLUE}" "${title}" "${RESET}"
    if "$@" >"${log_file}" 2>&1; then
        rm -f "${log_file}"
        printf "%b完成：%s%b\n" "${GREEN}" "${title}" "${RESET}"
        return 0
    fi

    printf "%b错误日志：%s%b\n" "${RED}" "${title}" "${RESET}" >&2
    sed 's/^/    /' "${log_file}" >&2
    fail "${title}失败"
}

check_docker_login() {
    local log_file="${TEMP_DIR}/docker-login.log"

    printf "%b执行：验证 Docker Hub 登录状态%b\n" "${BLUE}" "${RESET}"
    if docker login </dev/null >"${log_file}" 2>&1; then
        rm -f "${log_file}"
        printf "%b完成：Docker Hub 登录状态有效%b\n" "${GREEN}" "${RESET}"
        return 0
    fi

    printf "%b错误日志：Docker Hub 登录失败%b\n" "${RED}" "${RESET}" >&2
    sed 's/^/    /' "${log_file}" >&2
    fail "请先执行 docker login 完成登录"
}

validate_image_name() {
    [[ "$1" =~ ^[a-z0-9]+([._-][a-z0-9]+)*/[a-z0-9]+([._-][a-z0-9]+)*$ ]] ||
        fail "Docker Hub 仓库格式不正确，请使用 namespace/repository"
}

validate_version() {
    [[ "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([.-][A-Za-z0-9][A-Za-z0-9_.-]*)?$ ]] ||
        fail "版本号格式不正确，请使用 v1.0.0 或 v1.0.0-rc.1"
}

prompt_inputs() {
    printf "%bDocker Hub 仓库 [%s]：%b" "${BLUE}" "${DEFAULT_IMAGE}" "${RESET}"
    IFS= read -r IMAGE_NAME
    IMAGE_NAME="${IMAGE_NAME:-${DEFAULT_IMAGE}}"
    validate_image_name "${IMAGE_NAME}"

    printf "%b请输入要推送的版本号（例如 v0.1.0）：%b" "${BLUE}" "${RESET}"
    IFS= read -r VERSION
    validate_version "${VERSION}"

    local publish_latest_answer
    printf "%b同时更新 latest 标签？[Y/n]：%b" "${BLUE}" "${RESET}"
    IFS= read -r publish_latest_answer
    case "${publish_latest_answer}" in
        ""|y|Y|yes|YES) PUBLISH_LATEST=true ;;
        n|N|no|NO) PUBLISH_LATEST=false ;;
        *) fail "请输入 y 或 n" ;;
    esac
}

check_local_image() {
    local arch="$1"
    local image="${IMAGE_NAME}:${VERSION}-linux-${arch}"
    local actual_platform

    docker image inspect "${image}" >/dev/null 2>&1 ||
        fail "未找到本地镜像 ${image}，请先执行 ./build.sh"

    actual_platform="$(docker image inspect "${image}" --format '{{.Os}}/{{.Architecture}}')"
    [[ "${actual_platform}" == "linux/${arch}" ]] ||
        fail "镜像 ${image} 的实际平台是 ${actual_platform}，预期 linux/${arch}"
}

confirm_push() {
    printf "\n%b推送配置%b\n" "${BLUE}" "${RESET}"
    printf "  Docker Hub 仓库：%s\n" "${IMAGE_NAME}"
    printf "  amd64 标签：%s:%s-linux-amd64\n" "${IMAGE_NAME}" "${VERSION}"
    printf "  arm64 标签：%s:%s-linux-arm64\n" "${IMAGE_NAME}" "${VERSION}"
    printf "  多架构标签：%s:%s\n" "${IMAGE_NAME}" "${VERSION}"
    if ${PUBLISH_LATEST}; then
        printf "  同步标签：%s:latest\n" "${IMAGE_NAME}"
    fi
    printf "\n同名远端标签将被更新。请输入 PUSH 确认："

    local confirmation
    IFS= read -r confirmation
    [[ "${confirmation}" == "PUSH" ]] || fail "已取消推送"
}

push_arch_image() {
    local arch="$1"
    local image="${IMAGE_NAME}:${VERSION}-linux-${arch}"

    run_quiet "推送 ${image}" docker push "${image}"
}

publish_manifest() {
    local -a args=(
        create
        --tag "${IMAGE_NAME}:${VERSION}"
    )

    if ${PUBLISH_LATEST}; then
        args+=(--tag "${IMAGE_NAME}:latest")
    fi
    args+=(
        "${IMAGE_NAME}:${VERSION}-linux-amd64"
        "${IMAGE_NAME}:${VERSION}-linux-arm64"
    )

    run_quiet "发布多架构镜像索引" docker buildx imagetools "${args[@]}"
}

main() {
    [[ $# -eq 0 ]] || fail "此脚本为交互式脚本，不接受命令行参数"
    [[ -t 0 ]] || fail "请在交互终端中运行此脚本"

    command_exists docker || fail "未找到 docker 命令"
    docker info >/dev/null 2>&1 || fail "Docker 未运行，请先启动 Docker Desktop"
    docker buildx version >/dev/null 2>&1 || fail "当前 Docker 未提供 buildx"
    TEMP_DIR="$(mktemp -d)"

    prompt_inputs
    check_local_image amd64
    check_local_image arm64

    check_docker_login

    confirm_push
    push_arch_image amd64
    push_arch_image arm64
    publish_manifest

    run_quiet "验证远端多架构标签" docker buildx imagetools inspect "${IMAGE_NAME}:${VERSION}"
    printf "%b完成：%s:%s 已发布%b\n" "${GREEN}" "${IMAGE_NAME}" "${VERSION}" "${RESET}"
}

main "$@"
