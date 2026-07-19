#!/usr/bin/env bash

set -Eeuo pipefail

IMAGE="${XIAOYUPOSTHUB_IMAGE:?缺少 XIAOYUPOSTHUB_IMAGE}"
NETWORK="${XIAOYUPOSTHUB_NETWORK:?缺少 XIAOYUPOSTHUB_NETWORK}"
DATABASE_URL="${DATABASE_URL:?缺少 DATABASE_URL}"
POSTGRES_IMAGE="${XPH_MIGRATION_POSTGRES_IMAGE:-postgres:18-alpine}"
TEMP_DIR="$(mktemp -d)"
SOURCE_CONTAINER=""

cleanup() {
    if [[ -n "${SOURCE_CONTAINER}" ]]; then
        docker rm -f "${SOURCE_CONTAINER}" >/dev/null 2>&1 || true
    fi
    rm -rf "${TEMP_DIR}"
}
trap cleanup EXIT

psql_run() {
    docker run --rm -i \
        --network "${NETWORK}" \
        -e DATABASE_URL="${DATABASE_URL}" \
        "${POSTGRES_IMAGE}" \
        sh -c 'exec psql "$DATABASE_URL" "$@"' sh "$@"
}

SOURCE_CONTAINER="$(docker create --entrypoint /bin/true "${IMAGE}")"
docker cp "${SOURCE_CONTAINER}:/app/migrations/." "${TEMP_DIR}/"
docker rm -f "${SOURCE_CONTAINER}" >/dev/null
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
