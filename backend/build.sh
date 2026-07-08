#!/usr/bin/env bash
# build.sh — 构建 XiaoyuPostHub 后端
#
# 流程：代码检查 → 单元测试 → 编译后端
# 输出：./bin/xph-backend
#
# 用法：
#   ./build.sh
#   ./build.sh --race
#   ./build.sh --arch amd64
#   ./build.sh --arch arm64
#   ./build.sh --os linux --arch amd64
#   ./build.sh --os linux --arch arm64
#   GOOS=linux GOARCH=arm64 ./build.sh

set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
LOG_DIR="${SCRIPT_DIR}/.build-logs"

BINARY_NAME="xph-backend"
TARGET_GOOS="${GOOS:-}"
TARGET_GOARCH="${GOARCH:-}"
RACE_ENABLED=false
OUTPUT_PATH=""

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
  ./build.sh --race
  ./build.sh --arch amd64
  ./build.sh --arch arm64
  ./build.sh --os linux --arch amd64
  ./build.sh --os linux --arch arm64
  GOOS=linux GOARCH=arm64 ./build.sh

参数：
  --os <linux|darwin|windows>   目标系统，默认当前系统
  --arch <amd64|arm64>          目标架构，默认当前架构；也支持 1 表示 amd64、2 表示 arm64
  --race                        启用竞态检测，仅支持当前系统和当前架构
  -h, --help                    查看帮助

说明：
  正常构建只显示阶段进度；命令详细输出会被隐藏。
  如果某一步失败，会自动打印该步骤的错误日志。
  参数统一使用空格形式，例如：--os linux --arch amd64
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

# sqlc 通常装在 $(go env GOPATH)/bin（默认 ~/go/bin），但用户 PATH 可能不含它。
# 优先在 GOPATH/bin 找一下，PATH 上没有就从这里取。
ensure_sqlc_in_path() {
	if command_exists sqlc; then
		return 0
	fi
	local gopath
	gopath="$(go env GOPATH 2>/dev/null || echo "")"
	if [[ -n "$gopath" && -x "${gopath}/bin/sqlc" ]]; then
		export PATH="${gopath}/bin:${PATH}"
	fi
}

validate_goos() {
    case "$1" in
        linux|darwin|windows) ;;
        *) fail "不支持的系统：$1（仅支持 linux / darwin / windows）" ;;
    esac
}

normalize_goarch() {
    case "$1" in
        1|amd64) printf "amd64\n" ;;
        2|arm64) printf "arm64\n" ;;
        *) fail "不支持的架构：$1（请选择 1/2，或使用 amd64 / arm64）" ;;
    esac
}

parse_args() {
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --race)
                RACE_ENABLED=true
                shift
                ;;
            --os)
                [[ -n "${2:-}" ]] || fail "--os 需要指定系统，例如：--os linux"
                TARGET_GOOS="$2"
                shift 2
                ;;
            --os=*)
                fail "请使用空格形式：--os linux"
                ;;
            --arch)
                [[ -n "${2:-}" ]] || fail "--arch 需要指定架构，例如：--arch amd64"
                TARGET_GOARCH="$(normalize_goarch "$2")"
                shift 2
                ;;
            --arch=*)
                fail "请使用空格形式：--arch amd64"
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

file_size() {
    wc -c < "$1" | tr -d ' '
}

check_race() {
    local host_goos
    local host_goarch

    host_goos="$(go env GOOS)"
    host_goarch="$(go env GOARCH)"

    if ${RACE_ENABLED}; then
        if [[ "${TARGET_GOOS}" != "${host_goos}" || "${TARGET_GOARCH}" != "${host_goarch}" ]]; then
            fail "竞态检测仅支持当前系统和当前架构；跨平台构建请去掉 --race"
        fi
    fi
}

build_binary() {
    mkdir -p bin

    local output_name="${BINARY_NAME}"
    local cgo_enabled
    local -a build_args

    if [[ "${TARGET_GOOS}" == "windows" ]]; then
        output_name="${BINARY_NAME}.exe"
    fi

    OUTPUT_PATH="./bin/${output_name}"

    if ${RACE_ENABLED}; then
        cgo_enabled="${CGO_ENABLED:-1}"
        build_args=(go build -trimpath -ldflags="-s -w" -race -o "${OUTPUT_PATH}" .)
    else
        cgo_enabled="${CGO_ENABLED:-0}"
        build_args=(go build -trimpath -ldflags="-s -w" -o "${OUTPUT_PATH}" .)
    fi

    run_command 4 4 "编译后端" "go-build" env \
        GOOS="${TARGET_GOOS}" \
        GOARCH="${TARGET_GOARCH}" \
        CGO_ENABLED="${cgo_enabled}" \
        "${build_args[@]}"
}

main() {
	parse_args "$@"

	command_exists go || fail "未找到 go 命令"
	ensure_sqlc_in_path
	command_exists sqlc || fail "未找到 sqlc 命令"

	TARGET_GOOS="${TARGET_GOOS:-$(go env GOOS)}"
    TARGET_GOARCH="${TARGET_GOARCH:-$(go env GOARCH)}"
    TARGET_GOARCH="$(normalize_goarch "${TARGET_GOARCH}")"

    validate_goos "${TARGET_GOOS}"
    check_race

    printf "%b后端构建配置%b\n" "${BLUE}" "${RESET}"
    printf "  目标系统：%s\n" "${TARGET_GOOS}"
    printf "  目标架构：%s\n" "${TARGET_GOARCH}"

run_command 1 4 "生成 sqlc 代码" "sqlc-generate" sqlc generate
	run_command 2 4 "执行代码检查" "go-vet" go vet ./...
	# -p=1 串行跑包：dbtest 每个包都 reset schema 并发跑会互踩
	run_command 3 4 "执行单元测试" "go-test" go test -p=1 ./...
	build_binary

    [[ -f "${OUTPUT_PATH}" ]] || fail "未找到后端产物：${OUTPUT_PATH}"

    print_success "后端构建完成" "产物 ${OUTPUT_PATH}，$(file_size "${OUTPUT_PATH}") 字节，${TARGET_GOOS}/${TARGET_GOARCH}"
}

main "$@"
