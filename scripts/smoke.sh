#!/usr/bin/env bash
# Phase 0 and 1 exit criteria, checked against a running stack.
set -euo pipefail

BASE="${AZIR_BASE:-http://localhost:8080}"
CANARY="AZIR-CANARY-smoke-b7f3e91d-DO-NOT-EMIT"

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { printf '%-2s %s\n' "$1" "$2"; }

step 1 "core, embedded nats and postgres are healthy"
health=$(curl -fsS "$BASE/healthz")
echo "$health" | grep -q '"status":"ok"' || fail "unhealthy: $health"

step 2 "echo plugin discovered via \$SRV"
curl -fsS "$BASE/api/registry" | grep -q '"name":"echo"' || fail "echo not discovered"

step 3 "an unapproved tool is not usable"
# Approvals persist across restarts by design, so reset before asserting —
# otherwise this only passes on a fresh volume.
for tool in ping leak; do
  curl -fsS -X POST "$BASE/api/capabilities/echo/$tool/decide" \
    -H 'Content-Type: application/json' -d '{"status":"pending"}' >/dev/null
done
curl -fsS "$BASE/api/registry" | grep -q '"status":"pending"' || fail "tool was not pending"
denied=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$BASE/api/invoke/echo/ping" \
  -H 'Content-Type: application/json' -d '{"customer_id":""}')
[ "$denied" = "403" ] || fail "unapproved tool was invocable (HTTP $denied)"

step 4 "an administrator can approve a capability"
curl -fsS -X POST "$BASE/api/capabilities/echo/ping/decide" \
  -H 'Content-Type: application/json' -d '{"status":"approved"}' >/dev/null
curl -fsS -X POST "$BASE/api/capabilities/echo/leak/decide" \
  -H 'Content-Type: application/json' -d '{"status":"approved"}' >/dev/null

step 5 "approved tool round-trips"
pong=$(curl -fsS -X POST "$BASE/api/invoke/echo/ping" \
  -H 'Content-Type: application/json' -d '{"args":{"message":"hello"}}')
echo "$pong" | grep -q 'hello' || fail "round trip failed: $pong"

step 6 "SDK redacts credential-shaped output"
leak=$(curl -fsS -X POST "$BASE/api/invoke/echo/leak" \
  -H 'Content-Type: application/json' -d '{}')
echo "$leak" | grep -q 'caught-by-key-name' && fail "key-name redaction did not run: $leak"
echo "$leak" | grep -q 'super-secret-api-key-value' && fail "literal redaction did not run: $leak"

step 7 "customer spine resolves an external identity"
cust=$(curl -fsS -X POST "$BASE/api/customers" \
  -H 'Content-Type: application/json' \
  -d "{\"display_name\":\"Smoke Test $RANDOM\"}")
cust_id=$(echo "$cust" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
[ -n "$cust_id" ] || fail "customer not created: $cust"
curl -fsS -X POST "$BASE/api/customers/$cust_id/identities" \
  -H 'Content-Type: application/json' \
  -d "{\"plugin\":\"syncro\",\"external_id\":\"smoke-$RANDOM\"}" >/dev/null

step 8 "credentials are stored sealed and never returned"
curl -fsS -X PUT "$BASE/api/credentials" -H 'Content-Type: application/json' \
  -d "{\"customer_id\":\"$cust_id\",\"plugin\":\"3cx\",\"kind\":\"extension_password\",\"secret\":\"$CANARY\"}" >/dev/null
listing=$(curl -fsS "$BASE/api/credentials")
echo "$listing" | grep -q "$CANARY" && fail "credential value returned by the API"

step 9 "the canary never appears in the container's logs"
if docker compose -f deploy/compose.yaml logs 2>/dev/null | grep -q "$CANARY"; then
  fail "canary credential reached the container logs"
fi

step 10 "pgvector is present and indexable"
docker compose -f deploy/compose.yaml exec -T postgres \
  psql -U azir -d azir -qtAc "SELECT '[1,2,3]'::vector <=> '[1,2,4]'::vector" >/dev/null \
  || fail "pgvector is not usable in the running database"

step 11 "actions were audited"
curl -fsS "$BASE/api/audit" | grep -q 'credential.put' || fail "audit did not record credential.put"

step 12 "the frontend is served by the same binary on the same port"
curl -fsS "$BASE/" | grep -qi '<div id="root">' || fail "embedded frontend not served"
curl -sS -o /dev/null -w '%{http_code}' "$BASE/api/nope" | grep -q 404 \
  || fail "an unknown api path fell through to the SPA"

echo
echo "PASS — discovery, approval gate, transport, redaction, spine, vault and audit verified"
