# ==============================================================================
# BUILD
# ==============================================================================

FROM ghcr.io/pyck-ai/baseimages/golang:1.26-alpine AS builder

ARG SERVICE_NAME

# This is for the docker-compose setup only. Remove this line we switch to k8s.
RUN apk add --no-cache busybox-static

# populate go module cache
WORKDIR /app/common
COPY ./backend/common/go.mod ./backend/common/go.sum ./
RUN --mount=type=cache,id=go-cache,target=/var/cache/go/ \
    --mount=type=cache,id=go-modcache,target=/go/pkg/mod/ \
    go mod download all

WORKDIR /app/bootstrap
COPY ./backend/bootstrap/go.mod ./backend/bootstrap/go.sum ./
RUN --mount=type=cache,id=go-cache,target=/var/cache/go/ \
    --mount=type=cache,id=go-modcache,target=/go/pkg/mod/ \
    go mod download all

WORKDIR /app/management
COPY ./backend/management/go.mod ./backend/management/go.sum ./
RUN --mount=type=cache,id=go-cache,target=/var/cache/go/ \
    --mount=type=cache,id=go-modcache,target=/go/pkg/mod/ \
    go mod download all

WORKDIR /app/temporal
COPY ./backend/common/go.mod ./backend/temporal/go.sum ./
RUN --mount=type=cache,id=go-cache,target=/var/cache/go/ \
    --mount=type=cache,id=go-modcache,target=/go/pkg/mod/ \
    go mod download all

WORKDIR /app/${SERVICE_NAME}
COPY ./backend/${SERVICE_NAME}/go.mod ./backend/${SERVICE_NAME}/go.sum ./
RUN --mount=type=cache,id=go-cache,target=/var/cache/go/ \
    --mount=type=cache,id=go-modcache,target=/go/pkg/mod/ \
    go mod download all

WORKDIR /app
COPY ./backend/common/ ./common/
COPY ./backend/bootstrap/ ./bootstrap/
COPY ./backend/management/ ./management/
COPY ./backend/${SERVICE_NAME}/ ./${SERVICE_NAME}/

WORKDIR /app/${SERVICE_NAME}
ARG GIT_COMMIT_SHA
ENV GIT_COMMIT_SHA=${GIT_COMMIT_SHA}
ARG GIT_COMMIT_DATE
ENV GIT_COMMIT_DATE=${GIT_COMMIT_DATE}
RUN --mount=type=cache,id=go-cache,target=/var/cache/go/ \
    --mount=type=cache,id=go-modcache,target=/go/pkg/mod/ \
    go mod verify && \
    go mod tidy -v -diff && \
    go build \
    -mod=readonly \
    -trimpath \
    -ldflags="-w -s -extldflags=-static -X=github.com/pyck-ai/pyck/backend/common/env.buildInfoGitCommitSHA=${GIT_COMMIT_SHA} -X=github.com/pyck-ai/pyck/backend/common/env.buildInfoGitCommitDate=${GIT_COMMIT_DATE}" \
    -o=/tmp/service \
    ./cmd/pyck-${SERVICE_NAME}


# ==============================================================================
# RUNTIME
# ==============================================================================

FROM ghcr.io/pyck-ai/baseimages/static:latest

ARG SERVICE_NAME
ARG SERVICE_PORT=8080

ENV PYCK_HTTP_PORT=${SERVICE_PORT}
ENV PYCK_ENV_DIR=/src
ENV PYCK_LOG_LEVEL=info
ENV PYCK_LOG_FORMAT=json
ENV PYCK_DATABASE_DEBUG=false

# TODO: we should not need nc in the final image, but it is currently used in the health check.
# This is for the docker-compose setup only. Remove this line we switch to k8s.
COPY --from=builder /bin/busybox.static /usr/local/bin/nc

WORKDIR /usr/local/bin/
COPY --from=builder /tmp/service ./service

WORKDIR /data
USER nonroot
ENTRYPOINT [ "/usr/local/bin/service" ]
CMD [ ]

