#!/usr/bin/env bash
set -Eeuo pipefail

cd "$(dirname "$0")/deploy"

docker compose -p xiaoyuposthub up -d --pull never --force-recreate
