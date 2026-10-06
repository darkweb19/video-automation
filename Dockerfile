# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/video-automation ./cmd/video-automation

FROM alpine:3.24
RUN apk add --no-cache ffmpeg su-exec \
	&& addgroup -S app && adduser -S -G app -h /app app \
	&& mkdir -p /data/videos /data/projects \
	&& chown app:app /data /data/videos /data/projects

COPY --from=build --chown=app:app /out/video-automation /app/video-automation
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh

ENV DATA_DIR=/data
WORKDIR /app

EXPOSE 8080

# Alpine includes BusyBox wget, so this does not require curl in the image.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
