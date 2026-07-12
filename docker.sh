#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "$0")/deploy"

run_step() {
    local title="$1"
    local log=""
    shift
    log="$(mktemp)"
    printf '执行：%s\n' "${title}"
    if ! "$@" >"${log}" 2>&1; then
        printf '错误：%s失败\n' "${title}" >&2
        sed 's/^/    /' "${log}" >&2
        rm -f "${log}"
        exit 1
    fi
    rm -f "${log}"
    printf '完成：%s\n' "${title}"
}

printf '执行：选择本地镜像\n'
case "$(uname -m)" in
    arm64|aarch64) arch="arm64" ;;
    x86_64|amd64) arch="amd64" ;;
    *) echo "不支持的架构：$(uname -m)" >&2; exit 1 ;;
esac

image="$(docker image ls "pylintech/xiaoyuposthub:*-linux-${arch}" --format '{{.Repository}}:{{.Tag}}' | head -n 1)"
[[ -n "${image}" ]] || {
    echo "未找到 linux/${arch} 本地镜像，请先执行 ./build.sh" >&2
    exit 1
}
docker image inspect "${image}" >/dev/null 2>&1 || {
    echo "未找到本地镜像 ${image}，请先执行 ./build.sh" >&2
    exit 1
}

printf '完成：选择本地镜像（%s）\n' "${image}"

printf '执行：写入本地配置\n'
cat > .env <<EOF
# XiaoyuPostHub 配置

# 数据库
DATABASE_URL=postgresql://xiaoyuposthub:qwer1234@host.docker.internal:5432/xiaoyuposthub

# 超级管理员
SUPER_ADMIN_USERNAME=admin
SUPER_ADMIN_PASSWORD_HASH='\$2a\$12\$iOW2FfH8UqW8S1xjGJ7t.Oqe08pPuUtMOtliALALGnfl6Q8pQA7cq'

# Docker
XIAOYUPOSTHUB_IMAGE=${image}
XIAOYUPOSTHUB_PORT=8080

# 前端
STATIC_DIR=/app/web

# HTTPS 使用 true，直接 HTTP 使用 false
SESSION_COOKIE_SECURE=false
EOF
printf '完成：写入本地配置\n'

run_step "启动本地容器" docker compose -p xiaoyuposthub up -d --pull never --force-recreate
printf '完成：本地服务已启动（http://localhost:8080，admin / qwer1234）\n'
