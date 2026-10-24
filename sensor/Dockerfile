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
  go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o sensor

FROM scratch

COPY --from=builder /app/sensor /strasboard-sensor
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY config.json /etc/strasboard/sensor.json

ENV CONFIG_PATH=/etc/strasboard/sensor.json
ENTRYPOINT ["/strasboard-sensor"]
