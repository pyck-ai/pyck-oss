# syntax=docker/dockerfile:1
# check=skip=SecretsUsedInArgOrEnv

#######################################################################
#   CONFIGURATION                                                     #
#######################################################################

ARG TEMPORAL_VERSION=1.29.3

#######################################################################
#   END OF CONFIGURATION                                              #
#                                                                     #
# Do not edit below this line unless you know what you are doing!     #
#######################################################################
FROM ghcr.io/pyck-ai/baseimages/alpine:latest AS builder

WORKDIR /app

WORKDIR /app/common
COPY ./backend/common/go.mod ./backend/common/go.sum ./
RUN --mount=type=cache,id=go-cache,target=/var/cache/go/ \
    --mount=type=cache,id=go-modcache,target=/go/pkg/mod/ \
    go mod download all
WORKDIR /app/temporal
COPY ./backend/temporal/go.mod ./backend/temporal/go.sum ./
RUN --mount=type=cache,id=go-cache,target=/var/cache/go/ \
    --mount=type=cache,id=go-modcache,target=/go/pkg/mod/ \
    go mod download all

WORKDIR /app
COPY ./backend/common/ ./common/
COPY ./backend/temporal/ ./temporal/
WORKDIR /app/temporal
RUN --mount=type=cache,id=go-cache,target=/var/cache/go/ \
    --mount=type=cache,id=go-modcache,target=/go/pkg/mod/ \
    go mod verify && \
    go mod tidy -v -diff && \
    CGO_ENABLED=0 go build \
    -mod=readonly \
    -trimpath \
    -ldflags="-w -s -extldflags=-static" \
    -o=/tmp/temporal-server \
    ./cmd/server/*.go


#################################################
#                                               #
#   runtime environment                         #
#                                               #
#################################################
FROM temporalio/server:${TEMPORAL_VERSION}

USER root
COPY --from=builder --chown=temporal:temporal --chmod=0755 \
    /tmp/temporal-server /usr/local/bin/temporal-server

USER temporal

ENV BIND_ON_IP=::0
ENV USE_INTERNAL_FRONTEND=1
ENV TEMPORAL_ADDRESS=:7236
ENV LOG_LEVEL=warn
ENV TEMPORAL_AUTH_AUTHORIZER=default

ENV PYCK_ENV_DIR=/src
ENV PYCK_EVENT_ADAPTER=default
ENV PYCK_LOG_LEVEL=info
