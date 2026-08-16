# Parameterised so every Go binary in cmd/ builds from one file. Plugins are
# ordinary containers that happen to speak NATS, so they need no special
# treatment here.
ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

# Dependencies first, so source edits do not invalidate the module layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG CMD=azir-core
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 azir
USER azir
COPY --from=build /out/app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
