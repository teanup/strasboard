# Build
FROM golang:1.21-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY *.go ./
COPY templates/ templates/
COPY static/ static/
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /app

# Build tzdata
FROM alpine:latest AS tzdata
RUN apk add --no-cache tzdata

# Run
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=tzdata /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /app /app
EXPOSE $PORT
ENTRYPOINT ["/app"]
