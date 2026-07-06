#!/usr/bin/env bash
# build.sh — 构建 XiaoyuPostHub 后端
# 流程：vet → test → build
# 输出：./bin/xph-backend
#
# 用法：
#   ./build.sh           # 标准构建
#   ./build.sh --race    # 启用 race detector（开发用）

set -euo pipefail

cd "$(dirname "$0")"

echo "==> Go version: $(go version)"

echo "==> go vet ./..."
go vet ./...

echo "==> go test ./..."
go test ./...

echo "==> go build"
mkdir -p bin

LDFLAGS="-s -w"   # 去掉符号表与调试信息，减小体积
RACE_FLAG=""
if [[ "${1:-}" == "--race" ]]; then
	RACE_FLAG="-race"
	echo "    (race detector enabled)"
fi

# shellcheck disable=SC2086  # RACE_FLAG 可能为空，需要 word splitting 让空参数被忽略
go build -trimpath -ldflags="$LDFLAGS" $RACE_FLAG -o ./bin/xph-backend .

size=$(stat -f "%z" ./bin/xph-backend 2>/dev/null || stat -c "%s" ./bin/xph-backend)
printf "==> 产物：./bin/xph-backend (%s 字节)\n" "$size"