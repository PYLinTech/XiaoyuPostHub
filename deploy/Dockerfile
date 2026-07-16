# XiaoyuPostHub Dockerfile

FROM alpine:3.20

ARG APP_UID=10001
ARG APP_GID=10001
ARG APP_USER=xiaoyuposthub
ARG APP_GROUP=xiaoyuposthub

ENV APP_HOME=/app \
    DATA_DIR=/data \
    APP_ENV=production \
    TZ=Asia/Shanghai

WORKDIR ${APP_HOME}

RUN apk add --no-cache ca-certificates tzdata \
    && cp /usr/share/zoneinfo/${TZ} /etc/localtime \
    && echo "${TZ}" > /etc/timezone \
    && addgroup -S -g ${APP_GID} ${APP_GROUP} \
    && adduser -S -D -H -u ${APP_UID} -G ${APP_GROUP} -s /sbin/nologin ${APP_USER} \
    && mkdir -p "${APP_HOME}" "${DATA_DIR}/uploads" \
    && chown root:${APP_GROUP} "${APP_HOME}" \
    && chmod 550 "${APP_HOME}" \
    && chown -R ${APP_USER}:${APP_GROUP} "${DATA_DIR}" \
    && chmod 750 "${DATA_DIR}" "${DATA_DIR}/uploads"

COPY app/xph-backend /app/xph-backend
COPY app/web /app/web

RUN chown root:${APP_GROUP} /app/xph-backend \
    && chmod 550 /app/xph-backend \
    && chown -R root:${APP_GROUP} /app/web \
    && find /app/web -type d -exec chmod 550 {} \; \
    && find /app/web -type f -exec chmod 440 {} \;

USER ${APP_USER}:${APP_GROUP}

EXPOSE 8080

VOLUME ["/data"]

ENTRYPOINT ["/app/xph-backend"]
