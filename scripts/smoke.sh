#!/usr/bin/env bash
# Phase 0 exit criteria, checked against a running stack.
set -euo pipefail

BASE="${AZIR_BASE:-http://localhost:8080}"
fail() { echo "FAIL: $*" >&2; exit 1; }

echo "1/4 core is healthy"
curl -fsS "$BASE/healthz" | grep -q '"ok"' || fail "core unhealthy"

echo "2/4 echo plugin discovered via \$SRV"
registry=$(curl -fsS "$BASE/api/registry")
echo "$registry" | grep -q '"name":"echo"' || fail "echo not discovered"

echo "3/4 tool call round-trips"
pong=$(curl -fsS -X POST "$BASE/api/invoke/echo/ping" \
  -H 'Content-Type: application/json' \
  -d '{"customer_id":"cust_demo","args":{"message":"hello"}}')
echo "$pong" | grep -q 'hello' || fail "round trip failed: $pong"

echo "4/4 SDK redacts credential-shaped output"
leak=$(curl -fsS -X POST "$BASE/api/invoke/echo/leak" \
  -H 'Content-Type: application/json' -d '{"customer_id":"cust_demo"}')
echo "$leak" | grep -q 'caught-by-key-name' && fail "key-name redaction did not run: $leak"
echo "$leak" | grep -q 'super-secret-api-key-value' && fail "literal redaction did not run: $leak"

echo
echo "PASS — discovery, transport and redaction all verified"
