# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/video-automation .

FROM alpine:3.24
RUN addgroup -S app && adduser -S -G app -h /app app \
    && mkdir -p /data \
    && chown -R app:app /data

COPY --from=build --chown=app:app /out/video-automation /app/video-automation

ENV DATA_DIR=/data
WORKDIR /app
USER app

EXPOSE 8080

# Alpine includes BusyBox wget, so this does not require curl in the image.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["/app/video-automation"]
