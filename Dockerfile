# syntax=docker/dockerfile:1

FROM golang:1.27-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/video-automation ./cmd/video-automation

FROM alpine:3.24

# Alpine's signed v3.24/community APK index pins and verifies the musl-native Deno package.
# The yt-dlp Python wheels are independently SHA-256 checked from the lock file below.
# The importer sets HOME=/nonexistent to suppress user config. Each import gives Deno
# a private, temporary DENO_DIR under /tmp for the analysis cache.
COPY tools/clipping/requirements-youtube.lock /usr/local/share/video-automation/youtube-runtime.lock
COPY tools/clipping/check_youtube_runtime_contract.py /usr/local/share/video-automation/check_youtube_runtime_contract.py
RUN apk add --no-cache ffmpeg su-exec python3 py3-pip deno=2.7.4-r2 \
	&& addgroup -S app && adduser -S -G app -h /app app \
	&& mkdir -p /data/videos /data/projects \
	&& chown app:app /data /data/videos /data/projects \
	&& python3 -m pip install --no-cache-dir --break-system-packages --no-deps --only-binary=:all: --require-hashes --target=/opt/yt-dlp -r /usr/local/share/video-automation/youtube-runtime.lock \
	&& chmod -R a+rX /opt/yt-dlp \
	&& printf '%s\n' '#!/bin/sh' 'export PYTHONPATH="/opt/yt-dlp${PYTHONPATH:+:$PYTHONPATH}"' 'export PYTHONDONTWRITEBYTECODE=1' 'exec python3 -m yt_dlp "$@"' > /usr/local/bin/yt-dlp \
	&& chmod 0755 /usr/local/bin/yt-dlp \
	&& su-exec app:app sh -c 'test -r /opt/yt-dlp/yt_dlp/YoutubeDL.py && test -r /opt/yt-dlp/yt_dlp_ejs/yt/solver/core.min.js && test ! -w /opt/yt-dlp && test ! -w /opt/yt-dlp/yt_dlp/YoutubeDL.py && test ! -w /opt/yt-dlp/yt_dlp_ejs/yt/solver/core.min.js && test -x /usr/local/bin/yt-dlp && test -x /usr/bin/deno && test -w /tmp' \
	&& test "$(su-exec app:app /usr/local/bin/yt-dlp --version)" = "2026.08.19" \
	&& deno_dir="$(mktemp -d /tmp/youtube-deno-dir-check.XXXXXX)" \
	&& chown app:app "$deno_dir" \
	&& su-exec app:app sh -c 'DENO_DIR="$1" /usr/bin/deno info > "$1/info.txt" && grep -F "$1" "$1/info.txt" >/dev/null' sh "$deno_dir" \
	&& rm -rf "$deno_dir" \
	&& test "$(command -v yt-dlp)" = "/usr/local/bin/yt-dlp" \
	&& test "$(command -v deno)" = "/usr/bin/deno" \
	&& test "$(yt-dlp --version)" = "2026.08.19" \
	&& PYTHONPATH=/opt/yt-dlp python3 -c 'from importlib.metadata import version; assert version("yt-dlp") == "2026.8.19"; assert version("yt-dlp-ejs") == "0.8.0"' \
	&& yt-dlp --help | grep -q -- '--no-remote-components' \
	&& yt-dlp --help | grep -q -- '--js-runtimes' \
	&& deno --version | grep -q -E '^deno 2\.7\.4( |$)' \
	&& su-exec app:app env HOME=/nonexistent PYTHONDONTWRITEBYTECODE=1 /usr/bin/python3 /usr/local/share/video-automation/check_youtube_runtime_contract.py

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
