#!/usr/bin/env bash
# Boots the built image against a real database and asserts the whole thing
# works as one container: embedded NATS, supervised plugins, the API, and the
# embedded UI.
#
# The database is Postgres and not an afterthought — pgvector is why. Starting
# the image without one is how this check spent every run of its life failing
# on "DATABASE_URL is required" before it had asserted anything at all.
set -euo pipefail

IMAGE="${1:-azir:ci}"
NAME="azir-ci-$$"
DB="azir-ci-db-$$"
NET="azir-ci-net-$$"
KEY=$(openssl rand -base64 32)

cleanup() {
  docker rm -f "$NAME" "$DB" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker network create "$NET" >/dev/null

# The same image the deployment runs, so the extensions the migrations create
# are the ones this proves are there.
docker run -d --name "$DB" --network "$NET" \
  -e POSTGRES_USER=azir -e POSTGRES_PASSWORD=ci -e POSTGRES_DB=azir \
  pgvector/pgvector:pg18 >/dev/null

for _ in $(seq 1 60); do
  if docker exec "$DB" pg_isready -U azir -d azir >/dev/null 2>&1; then break; fi
  sleep 1
done
docker exec "$DB" pg_isready -U azir -d azir >/dev/null 2>&1 \
  || { echo "the database never came up"; docker logs "$DB"; exit 1; }

docker run -d --name "$NAME" --network "$NET" -p 18080:8080 \
  -e AZIR_MASTER_KEY="$KEY" \
  -e DATABASE_URL="postgres://azir:ci@$DB:5432/azir?sslmode=disable" \
  "$IMAGE" >/dev/null

for _ in $(seq 1 60); do
  if curl -fsS http://localhost:18080/healthz >/dev/null 2>&1; then break; fi
  sleep 1
done

curl -fsS http://localhost:18080/healthz | grep -q '"status":"ok"' \
  || { echo "unhealthy"; docker logs "$NAME"; exit 1; }

# Nothing about the deployment is readable without a session. This used to
# fetch the registry and read the plugin list out of it, which stopped working
# the day the registry was put behind authentication — and the check had been
# failing for other reasons the whole time, so nobody found out. Asserting the
# closed door is the better test anyway.
code=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:18080/api/registry)
[ "$code" = "401" ] \
  || { echo "the registry answered $code to a request with no session"; exit 1; }

# Supervision, from the container's own log: every bundled plugin connects and
# registers its tools, or the supervisor is not doing its job.
#
# "registered tool" rather than the plugin's own "plugin ready", because the
# first is core saying it saw the registration and the second is the plugin's
# stderr forwarded through the supervisor — which arrives seconds later and
# made this fail on a machine under load.
#
# Waited for either way. The API answers healthy as soon as it is listening,
# which is before anything it supervises has connected.
#
# Not `docker logs … | grep -q`. Under `set -o pipefail` that reports failure
# when it succeeds: grep -q exits the moment it matches, docker logs takes a
# SIGPIPE for the rest of its output, and the pipeline's status becomes the
# signal. The check looked exactly like a plugin that had not come up.
for want in echo 3cx syncro; do
  ready=""
  for _ in $(seq 1 45); do
    logs=$(docker logs "$NAME" 2>&1)
    case $logs in
      *"\"msg\":\"registered tool\",\"plugin\":\"$want\""*) ready=yes; break ;;
    esac
    sleep 1
  done
  [ -n "$ready" ] || { echo "the $want plugin registered nothing"; docker logs "$NAME"; exit 1; }
done

curl -fsS http://localhost:18080/ | grep -qi '<div id="root">' \
  || { echo "embedded frontend not served"; exit 1; }

echo "image check passed"
