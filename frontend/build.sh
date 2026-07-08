#!/usr/bin/env bash
# build.sh — 构建 XiaoyuPostHub 前端
#
# 流程：安装依赖 → 编译前端
# 输出：./build/
#
# 用法：
#   ./build.sh

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_DIR="${SCRIPT_DIR}/.build-logs"

cd "${SCRIPT_DIR}"

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

说明：
  正常构建只显示阶段进度；命令详细输出会被隐藏。
  如果某一步失败，会自动打印该步骤的错误日志。
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

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
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
    local step_no="$1"
    local step_total="$2"
    local title="$3"
    local log_name="$4"
    shift 4

    local log_file="${LOG_DIR}/${log_name}.log"

    mkdir -p "${LOG_DIR}"
    rm -f "${log_file}"

    print_step_start "${step_no}" "${step_total}" "${title}"

    if "$@" >"${log_file}" 2>&1; then
        rm -f "${log_file}"
        print_step_success "${step_no}" "${step_total}" "${title}"
    else
        show_log "${log_file}"
        fail "${title} 失败"
    fi
}

dir_size() {
    du -sh "$1" 2>/dev/null | awk '{print $1}'
}

main() {
    parse_args "$@"

    command_exists yarn || fail "未找到 yarn 命令"

    run_command 1 2 "安装前端依赖" "yarn-install" yarn install --frozen-lockfile
    run_command 2 2 "编译前端" "yarn-build" yarn build

    [[ -d build ]] || fail "未找到前端产物：./build/"

    print_success "前端构建完成" "产物 ./build/，$(dir_size build)"
}

main "$@"
