# syntax=docker/dockerfile:1

# ---- Build stage -----------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies are vendored: no network access is needed to build.
COPY go.mod go.sum ./
COPY vendor ./vendor
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web

ENV CGO_ENABLED=0
RUN go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/focus ./cmd/focus

# ---- Runtime stage ---------------------------------------------------------
FROM alpine:3.22
RUN apk add --no-cache tzdata ca-certificates \
 && addgroup -S focus && adduser -S -G focus -h /data focus \
 && mkdir -p /data && chown focus:focus /data

COPY --from=build /out/focus /usr/local/bin/focus

USER focus
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8765

ENV HOST=0.0.0.0 \
    PORT=8765 \
    DB_PATH=/data/focus.db

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -q -O /dev/null "http://127.0.0.1:${PORT}/api/auth/status" || exit 1

ENTRYPOINT ["focus"]
CMD ["daemon"]
