#!/usr/bin/env bash

set -Eeuo pipefail

IMAGE="${XIAOYUPOSTHUB_IMAGE:?缺少 XIAOYUPOSTHUB_IMAGE}"
NETWORK="${XIAOYUPOSTHUB_NETWORK:?缺少 XIAOYUPOSTHUB_NETWORK}"
DATABASE_URL="${DATABASE_URL:?缺少 DATABASE_URL}"
POSTGRES_IMAGE="${XPH_MIGRATION_POSTGRES_IMAGE:-postgres:18-alpine}"
TEMP_DIR="$(mktemp -d)"
SOURCE_CONTAINER=""

# postgres 官方镜像声明了数据目录卷（18.x 为 /var/lib/postgresql），psql 临时容器每次
# 运行都会因此生成一个随机命名的匿名卷，容器被强删或脚本中断时就会残留在宿主机上。
# 用 tmpfs 覆盖镜像声明的卷路径，从源头避免产生匿名卷。
PSQL_TMPFS_ARGS=()
while IFS= read -r volume_path; do
    [[ -n "${volume_path}" ]] || continue
    PSQL_TMPFS_ARGS+=(--tmpfs "${volume_path}")
done < <(docker image inspect --format '{{range $path, $_ := .Config.Volumes}}{{println $path}}{{end}}' "${POSTGRES_IMAGE}" 2>/dev/null || true)
if (( ${#PSQL_TMPFS_ARGS[@]} == 0 )); then
    PSQL_TMPFS_ARGS=(--tmpfs /var/lib/postgresql)
fi

cleanup() {
    if [[ -n "${SOURCE_CONTAINER}" ]]; then
        docker rm -fv "${SOURCE_CONTAINER}" >/dev/null 2>&1 || true
    fi
    rm -rf "${TEMP_DIR}"
}
trap cleanup EXIT

psql_run() {
    docker run --rm -i \
        --network "${NETWORK}" \
        "${PSQL_TMPFS_ARGS[@]}" \
        -e DATABASE_URL="${DATABASE_URL}" \
        "${POSTGRES_IMAGE}" \
        sh -c 'exec psql "$DATABASE_URL" "$@"' sh "$@"
}

# 应用镜像同样声明了 VOLUME /data，用 tmpfs 覆盖以避免产生匿名卷。
SOURCE_CONTAINER="$(docker create --tmpfs /data --entrypoint /bin/true "${IMAGE}")"
docker cp "${SOURCE_CONTAINER}:/app/migrations/." "${TEMP_DIR}/"
docker rm -fv "${SOURCE_CONTAINER}" >/dev/null
SOURCE_CONTAINER=""

psql_run -v ON_ERROR_STOP=1 -q -c '
CREATE TABLE IF NOT EXISTS xph_schema_migrations (
    filename   TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);'

FOUND_SQL=false
while IFS= read -r sql_file; do
    FOUND_SQL=true
    filename="$(basename "${sql_file}")"
    applied="$(printf '%s\n' \
        "SELECT 1 FROM xph_schema_migrations WHERE filename = :'filename';" \
        | psql_run -Atq -v ON_ERROR_STOP=1 -v filename="${filename}")"
    if [[ "${applied}" == "1" ]]; then
        printf '跳过：%s\n' "${filename}"
        continue
    fi

    printf '迁移：%s\n' "${filename}"
    {
        printf 'BEGIN;\n'
        printf "SELECT pg_advisory_xact_lock(hashtext('xiaoyuposthub_schema_migrations'));\n"
        cat "${sql_file}"
        printf '\nINSERT INTO xph_schema_migrations (filename) VALUES ('
        printf "'%s'" "${filename}"
        printf ');\nCOMMIT;\n'
    } | psql_run -v ON_ERROR_STOP=1 -q
done < <(find "${TEMP_DIR}" -maxdepth 1 -type f -name '[0-9][0-9][0-9].sql' | LC_ALL=C sort)

if [[ "${FOUND_SQL}" != true ]]; then
    printf '错误：目标镜像不包含数字迁移 SQL\n' >&2
    exit 1
fi

printf '数据库迁移完成\n'
