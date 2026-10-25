# syntax=docker/dockerfile:1
FROM --platform=${BUILDPLATFORM} golang:1.27 AS builder

ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
WORKDIR /app

COPY go.mod go.sum .
RUN --mount=type=cache,target=/go/pkg/mod \
  go mod download

COPY --parents main.go */* .
RUN --mount=type=cache,target=/go/pkg/mod \
  --mount=type=cache,target=/root/.cache/go-build \
  GOOS=${TARGETOS} GOARCH=${TARGETARCH} CGO_ENABLED=0 \
  go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o api

FROM alpine:latest AS tzdata

RUN apk add --no-cache tzdata

FROM scratch

COPY --from=builder /app/api /strasboard-api
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=tzdata /usr/share/zoneinfo /usr/share/zoneinfo
COPY config.json /etc/strasboard/api.json

ENV CONFIG_PATH=/etc/strasboard/api.json
EXPOSE 8080
ENTRYPOINT ["/strasboard-api"]
