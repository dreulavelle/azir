#!/usr/bin/env bash
# Boots the built image and asserts the whole thing works as one container:
# embedded NATS, SQLite, supervised plugins, the API, and the embedded UI.
set -euo pipefail

IMAGE="${1:-azir:ci}"
NAME="azir-ci-$$"
KEY=$(openssl rand -base64 32)

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --name "$NAME" -p 18080:8080 -e AZIR_MASTER_KEY="$KEY" "$IMAGE" >/dev/null

for _ in $(seq 1 40); do
  if curl -fsS http://localhost:18080/healthz >/dev/null 2>&1; then break; fi
  sleep 1
done

curl -fsS http://localhost:18080/healthz | grep -q '"status":"ok"' \
  || { echo "unhealthy"; docker logs "$NAME"; exit 1; }

curl -fsS http://localhost:18080/api/registry | grep -q '"name":"echo"' \
  || { echo "bundled plugin was not discovered"; docker logs "$NAME"; exit 1; }

curl -fsS http://localhost:18080/ | grep -qi '<div id="root">' \
  || { echo "embedded frontend not served"; exit 1; }

echo "image check passed"
