#!/bin/sh
set -eu

app=/app/video-automation

if [ "$(id -u)" -ne 0 ]; then
	exec "$app" "$@"
fi

# This command is read-only and must validate Railway mount metadata and path
# containment before the entrypoint creates or changes anything under DATA_DIR.
data_dir=$("$app" storage-path) || {
	echo "startup refused: persistent storage path validation failed" >&2
	exit 1
}

# Keep a second boundary in the shell in case the CLI contract or environment
# is misconfigured. Railway and Compose both mount the app volume at /data.
case "$data_dir" in
	/data|/data/*) ;;
	*) echo "startup refused: validated storage path is outside /data" >&2; exit 1 ;;
esac

if [ -L "$data_dir" ]; then
	echo "startup refused: persistent storage path must not be a symlink" >&2
	exit 1
fi

for path in "$data_dir/videos" "$data_dir/projects" "$data_dir/app.db" "$data_dir/app.db-wal" "$data_dir/app.db-shm" "$data_dir/secret.key"; do
	if [ -L "$path" ]; then
		echo "startup refused: persistent storage contains an unexpected symlink" >&2
		exit 1
	fi
done

mkdir -p "$data_dir/videos" "$data_dir/projects"
# Only touch the validated root, the two required directories, and known
# database/key files. Never recursively walk potentially large media folders.
chown -h app:app "$data_dir" "$data_dir/videos" "$data_dir/projects"
chmod 0700 "$data_dir" "$data_dir/videos" "$data_dir/projects"
for path in "$data_dir/app.db" "$data_dir/app.db-wal" "$data_dir/app.db-shm" "$data_dir/secret.key"; do
	if [ -e "$path" ]; then
		chown -h app:app "$path"
		chmod 0600 "$path"
	fi
done

exec su-exec app:app "$app" "$@"
