#!/bin/sh
set -e

APP_NAME="breakthewaves"
IMAGE_NAME="breakthewaves:latest"
WORKDIR="/root/Sea-BreakTheWaves"
PORT="20721:20721"

cd "$WORKDIR" || exit 1

echo "==> docker build"
docker build -t "$IMAGE_NAME" .

echo "==> remove old container"
if docker ps -a --format '{{.Names}}' | grep -q "^${APP_NAME}$"; then
    docker rm -f "$APP_NAME"
fi

echo "==> docker run"
docker run -d \
  --name "$APP_NAME" \
  -p "$PORT" \
  --restart always \
  "$IMAGE_NAME"

echo "==> container started"
docker ps | grep "$APP_NAME" || true