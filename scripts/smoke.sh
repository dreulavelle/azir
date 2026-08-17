#!/usr/bin/env bash
# Phase 0 and 1 exit criteria, checked against a running stack.
set -euo pipefail

BASE="${AZIR_BASE:-http://localhost:8080}"
CANARY="AZIR-CANARY-smoke-b7f3e91d-DO-NOT-EMIT"
JAR=$(mktemp)
trap 'rm -f "$JAR"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { printf '%-2s %s\n' "$1" "$2"; }

# Everything below the setup routes requires a session, so sign in first —
# either completing first-run setup or logging in as the smoke account.
#
# Read from .env, which `make init` generates and version control ignores.
# There is deliberately no fallback value: a default password in a repository
# is a working account on every deployment that never changed it.
if [ -f .env ]; then
  # shellcheck disable=SC1091 # generated locally, not in version control
  set -a && . ./.env && set +a
fi

SMOKE_EMAIL="${AZIR_SMOKE_EMAIL:-}"
SMOKE_PASS="${AZIR_SMOKE_PASSWORD:-}"
if [ -z "$SMOKE_EMAIL" ] || [ -z "$SMOKE_PASS" ]; then
  fail "set AZIR_SMOKE_EMAIL and AZIR_SMOKE_PASSWORD, or run \`make init\` to generate .env"
fi

if curl -fsS "$BASE/api/setup" | grep -q '"needs_setup":true'; then
  curl -fsS -c "$JAR" -X POST "$BASE/api/setup" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$SMOKE_EMAIL\",\"display_name\":\"Smoke\",\"password\":\"$SMOKE_PASS\"}" >/dev/null \
    || fail "first-run setup failed"
else
  curl -fsS -c "$JAR" -X POST "$BASE/api/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$SMOKE_EMAIL\",\"password\":\"$SMOKE_PASS\"}" >/dev/null \
    || fail "could not sign in as $SMOKE_EMAIL.
  This deployment already has accounts, so first-run setup is closed. Point
  AZIR_SMOKE_EMAIL and AZIR_SMOKE_PASSWORD in .env at an administrator, or
  start from a clean stack with: make down-hard && make up"
fi

# Authenticated wrappers.
get()  { curl -fsS -b "$JAR" "$@"; }
post() { curl -fsS -b "$JAR" -X POST "$@"; }
put()  { curl -fsS -b "$JAR" -X PUT "$@"; }

step 1 "core, embedded nats and postgres are healthy"
health=$(curl -fsS "$BASE/healthz")
echo "$health" | grep -q '"status":"ok"' || fail "unhealthy: $health"

step 2 "echo plugin discovered via \$SRV"
get "$BASE/api/registry" | grep -q '"name":"echo"' || fail "echo not discovered"

step 3 "an unapproved tool is not usable"
# Approvals persist across restarts by design, so reset before asserting —
# otherwise this only passes on a fresh volume.
for tool in ping leak; do
  post "$BASE/api/capabilities/echo/$tool/decide" \
    -H 'Content-Type: application/json' -d '{"status":"pending"}' >/dev/null
done
get "$BASE/api/registry" | grep -q '"status":"pending"' || fail "tool was not pending"
denied=$(curl -sS -b "$JAR" -o /dev/null -w '%{http_code}' -X POST "$BASE/api/invoke/echo/ping" \
  -H 'Content-Type: application/json' -d '{"customer_id":""}')
[ "$denied" = "403" ] || fail "unapproved tool was invocable (HTTP $denied)"

step 4 "an administrator can approve a capability"
post "$BASE/api/capabilities/echo/ping/decide" \
  -H 'Content-Type: application/json' -d '{"status":"approved"}' >/dev/null
post "$BASE/api/capabilities/echo/leak/decide" \
  -H 'Content-Type: application/json' -d '{"status":"approved"}' >/dev/null

step 5 "plugin settings are schema-driven and round-trip"
# Settings persist, so pin them to a known state rather than assuming a fresh
# database — the same reason approvals are reset above.
put "$BASE/api/plugins/echo/settings" -H 'Content-Type: application/json' \
  -d "{\"values\":{\"greeting\":\"pong\",\"shout\":false,\"demo_secret\":\"$CANARY\"}}" >/dev/null
get "$BASE/api/plugins/echo/settings" | grep -q '"x-azir-secret"' \
  || fail "plugin did not publish a config schema"
get "$BASE/api/plugins/echo/settings" | grep -q "$CANARY" \
  && fail "a stored secret was returned by the settings API"
get "$BASE/api/plugins/echo/settings" | grep -q '"demo_secret": *true' \
  || fail "secret was not routed to the vault"

step 6 "approved tool round-trips and reads its settings"
pong=$(post "$BASE/api/invoke/echo/ping" \
  -H 'Content-Type: application/json' -d '{"args":{"message":"hello"}}')
echo "$pong" | grep -qi 'hello' || fail "round trip failed: $pong"

step 7 "plugin resolves its credential from the vault"
post "$BASE/api/capabilities/echo/secret.check/decide" \
  -H 'Content-Type: application/json' -d '{"status":"approved"}' >/dev/null
check=$(post "$BASE/api/invoke/echo/secret.check" \
  -H 'Content-Type: application/json' -d '{}')
echo "$check" | grep -q '"configured":true' || fail "vault resolution failed: $check"
echo "$check" | grep -q "$CANARY" && fail "the credential value was returned"

step 8 "SDK redacts credential-shaped output"
leak=$(post "$BASE/api/invoke/echo/leak" \
  -H 'Content-Type: application/json' -d '{}')
echo "$leak" | grep -q 'caught-by-key-name' && fail "key-name redaction did not run: $leak"
echo "$leak" | grep -q 'super-secret-api-key-value' && fail "literal redaction did not run: $leak"

step 9 "customer spine resolves an external identity"
cust=$(post "$BASE/api/customers" \
  -H 'Content-Type: application/json' \
  -d "{\"display_name\":\"Smoke Test $RANDOM\"}")
cust_id=$(echo "$cust" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
[ -n "$cust_id" ] || fail "customer not created: $cust"
post "$BASE/api/customers/$cust_id/identities" \
  -H 'Content-Type: application/json' \
  -d "{\"plugin\":\"syncro\",\"external_id\":\"smoke-$RANDOM\"}" >/dev/null

step 10 "credentials are stored sealed and never returned"
put "$BASE/api/credentials" -H 'Content-Type: application/json' \
  -d "{\"customer_id\":\"$cust_id\",\"plugin\":\"3cx\",\"kind\":\"extension_password\",\"secret\":\"$CANARY\"}" >/dev/null
listing=$(get "$BASE/api/credentials")
echo "$listing" | grep -q "$CANARY" && fail "credential value returned by the API"

step 11 "the canary never appears in the container's logs"
if docker compose -f deploy/compose.yaml logs 2>/dev/null | grep -q "$CANARY"; then
  fail "canary credential reached the container logs"
fi

step 12 "pgvector is present and indexable"
docker compose -f deploy/compose.yaml exec -T postgres \
  psql -U azir -d azir -qtAc "SELECT '[1,2,3]'::vector <=> '[1,2,4]'::vector" >/dev/null \
  || fail "pgvector is not usable in the running database"

step 13 "actions were audited"
get "$BASE/api/audit" | grep -q 'credential.put' || fail "audit did not record credential.put"

step 14 "an unauthenticated request is refused"
curl -sS -o /dev/null -w '%{http_code}' "$BASE/api/registry" | grep -q 401 \
  || fail "an unauthenticated request reached the registry"

step 15 "the frontend is served by the same binary on the same port"
curl -fsS "$BASE/" | grep -qi '<div id="root">' || fail "embedded frontend not served"
curl -sS -b "$JAR" -o /dev/null -w '%{http_code}' "$BASE/api/nope" | grep -q 404 \
  || fail "an unknown api path fell through to the SPA"

echo
echo "PASS — discovery, approval gate, transport, redaction, spine, vault and audit verified"
