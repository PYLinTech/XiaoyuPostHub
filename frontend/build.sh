#!/usr/bin/env bash
# build.sh — 构建 XiaoyuPostHub 前端
# 流程：install → build
# 输出：./build/
#
# 用法：
#   ./build.sh

set -euo pipefail

cd "$(dirname "$0")"

echo "==> yarn 版本: $(yarn --version)"

echo "==> yarn install --frozen-lockfile"
yarn install --frozen-lockfile

echo "==> yarn build"
yarn build

# 产物大小
if [ -d build ]; then
	size=$(du -sh build 2>/dev/null | cut -f1)
	printf "==> 产物：./build/ (%s)\n" "$size"
fi