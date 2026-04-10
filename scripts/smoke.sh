#!/usr/bin/env bash
# scripts/smoke.sh — End-to-end smoke test for pagefault.
#
# Starts the binary against configs/minimal.yaml, exercises every Phase-1
# endpoint with curl, then stops the server. Fails (exit non-zero) on the
# first unexpected status code or missing response field.

set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/bin/pagefault"
CFG="$ROOT/configs/minimal.yaml"
HOST="127.0.0.1"
PORT="8444"
URL="http://$HOST:$PORT"

if [ ! -x "$BIN" ]; then
  echo "smoke: binary not found at $BIN (run 'make build' first)"
  exit 1
fi

echo "smoke: starting server"
"$BIN" serve --config "$CFG" >"$ROOT/bin/smoke.log" 2>&1 &
PID=$!
trap 'kill -TERM $PID 2>/dev/null || true' EXIT

# Wait up to ~2s for the health endpoint to come up.
for i in 1 2 3 4 5 6 7 8 9 10; do
  if curl -sf "$URL/health" >/dev/null 2>&1; then
    break
  fi
  sleep 0.2
done

fail() { echo "smoke FAIL: $*"; exit 1; }

check() {
  local label="$1" path="$2" body="${3:-}"
  local expected="${4:-200}"
  local code
  if [ -n "$body" ]; then
    code=$(curl -s -o "$ROOT/bin/smoke.resp" -w '%{http_code}' \
      -X POST -H 'Content-Type: application/json' -d "$body" "$URL$path")
  else
    code=$(curl -s -o "$ROOT/bin/smoke.resp" -w '%{http_code}' "$URL$path")
  fi
  if [ "$code" != "$expected" ]; then
    cat "$ROOT/bin/smoke.resp" || true
    fail "$label: expected $expected, got $code"
  fi
  echo "smoke: $label OK ($code)"
}

check "health"        "/health"
check "list_contexts" "/api/list_contexts" "{}"
check "search"        "/api/search"        '{"query":"pagefault"}'
check "read"          "/api/read"          '{"uri":"memory://README.md"}'
check "get_context"   "/api/get_context"   '{"name":"demo"}'

# MCP initialize smoke
curl -s -o "$ROOT/bin/smoke.resp" \
  -X POST -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  "$URL/mcp"
grep -q '"result"' "$ROOT/bin/smoke.resp" || fail "mcp initialize: missing result field"
echo "smoke: mcp_initialize OK"

echo "smoke: all checks passed"
