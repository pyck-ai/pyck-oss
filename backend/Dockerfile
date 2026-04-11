#################################################
#                                               #
#   build environment                           #
#                                               #
#################################################
FROM ghcr.io/pyck-ai/baseimages/alpine:latest AS builder
ARG SERVICE_NAME

# populate go module cache
WORKDIR /app/common
COPY ./backend/common/go.mod ./backend/common/go.sum ./
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

# compile the service
WORKDIR /app
COPY ./backend/common/ ./common/
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


#################################################
#                                               #
#   runtime environment                         #
#                                               #
#################################################
FROM ghcr.io/pyck-ai/baseimages/alpine:latest
ARG SERVICE_NAME

# Set up migrations
WORKDIR /data/migrations/
COPY ./backend/${SERVICE_NAME}/ent/migrate/migrations/ ./

# Set up entrypoint
WORKDIR /usr/local/bin/
COPY --from=builder /tmp/service ./service
WORKDIR /data
RUN adduser -D service
USER service
CMD [ "/usr/local/bin/service" ]
ARG SERVICE_PORT=8080
ENV PYCK_HTTP_PORT=${SERVICE_PORT}
ENV PYCK_LOG_LEVEL=info
ENV PYCK_LOG_FORMAT=json
ENV PYCK_DATABASE_DEBUG=false
