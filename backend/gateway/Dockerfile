#################################################
#                                               #
#   build environment                           #
#                                               #
#################################################
FROM ghcr.io/pyck-ai/baseimages/debian:latest AS builder

WORKDIR /backend
COPY ./backend/ ./
RUN cd ./gateway && task generate


#################################################
#                                               #
#   runtime environment                         #
#                                               #
#################################################
FROM ghcr.io/pyck-ai/baseimages/debian:latest

WORKDIR /usr/local/bin/
ENV APOLLO_TELEMETRY_DISABLED=true
ENV APOLLO_ELV2_LICENSE=accept
RUN curl -sSL https://router.apollo.dev/download/nix/latest | sh

WORKDIR /app/
COPY ./backend/gateway/ ./
COPY --from=builder /backend/gateway/supergraph.graphql supergraphs/pyck.graphql
CMD [ "router", "-c", "router-compose.yml", "-s", "./supergraphs/pyck.graphql" ]
