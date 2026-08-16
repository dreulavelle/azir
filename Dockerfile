# One image, one process tree. Core embeds NATS with JetStream, SQLite, the
# HTTP API and the built frontend; bundled plugins run as supervised children.
# Nothing else needs deploying.

ARG GO_VERSION=1.26
ARG NODE_VERSION=24
ARG ALPINE_VERSION=3.22

# ---------------------------------------------------------------- frontend --
FROM node:${NODE_VERSION}-alpine AS web
WORKDIR /web

# Manifest first so source edits do not invalidate the dependency layer.
COPY web/package.json ./
RUN npm install --no-audit --no-fund

COPY web/ ./
RUN npm run build

# ------------------------------------------------------------------ binaries --
FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# The built frontend is embedded into the binary, so the server has no asset
# directory to lose and no second origin to authorise.
COPY --from=web /web/dist ./web/dist

# CGO off: modernc.org/sqlite is pure Go, so the result is a static binary and
# the runtime image needs no C library.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/azir ./cmd/azir-core \
 && mkdir -p /out/plugins \
 && for dir in ./cmd/plugin-*; do \
      name=$(basename "$dir"); \
      go build -trimpath -ldflags="-s -w" -o "/out/plugins/$name" "$dir"; \
    done

# ------------------------------------------------------------------- runtime --
FROM alpine:${ALPINE_VERSION}

RUN apk add --no-cache ca-certificates tzdata wget \
 && adduser -D -u 10001 -h /var/lib/azir azir

COPY --from=build /out/azir /usr/local/bin/azir
COPY --from=build /out/plugins/ /usr/local/lib/azir/plugins/

# SQLite and JetStream both live here; it is the only thing worth backing up.
RUN mkdir -p /var/lib/azir && chown -R azir:azir /var/lib/azir
VOLUME /var/lib/azir

USER azir
WORKDIR /var/lib/azir
EXPOSE 8080

ENV AZIR_DATA_DIR=/var/lib/azir \
    AZIR_PLUGIN_DIR=/usr/local/lib/azir/plugins \
    AZIR_HTTP_ADDR=:8080

HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=3 \
    CMD wget --spider -q http://127.0.0.1:8080/healthz || exit 1

# No entrypoint script: the binary validates its own configuration, creates its
# own directories, and reports failures with real errors. A shell wrapper would
# only add a moving part to a design whose point is having fewer of them.
ENTRYPOINT ["/usr/local/bin/azir"]
