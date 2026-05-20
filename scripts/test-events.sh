#!/usr/bin/env bash
# Tests the httpcatch admin /events endpoint against a live server.
# Usage: ./scripts/test-events.sh [ADMIN_HOST] [ADMIN_PORT] [TOKEN]
set -euo pipefail

HOST="${1:-localhost}"
PORT="${2:-8081}"
TOKEN="${3:-${HTTPCATCH_TOKEN:-}}"
BASE="http://${HOST}:${PORT}"

if [[ -z "$TOKEN" ]]; then
  echo "Error: TOKEN required. Pass as third argument or set HTTPCATCH_TOKEN."
  exit 1
fi

GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

pass=0
fail=0

# assert_status <label> <expected_status> <curl_args...>
assert_status() {
  local label="$1"
  local want="$2"
  shift 2
  local got
  got=$(curl -s -o /dev/null -w "%{http_code}" "$@")
  if [[ "$got" == "$want" ]]; then
    echo -e "${GREEN}[PASS]${NC} $label  (HTTP $got)"
    ((pass++)) || true
  else
    echo -e "${RED}[FAIL]${NC} $label  (got $got, want $want)"
    ((fail++)) || true
  fi
}

# assert_body <label> <expected_status> <expected_substring> <curl_args...>
assert_body() {
  local label="$1"
  local want_status="$2"
  local want_substr="$3"
  shift 3
  local tmp status body
  tmp=$(mktemp)
  status=$(curl -s -o "$tmp" -w "%{http_code}" "$@")
  body=$(cat "$tmp")
  rm -f "$tmp"
  local ok=1
  [[ "$status" != "$want_status" ]] && ok=0
  [[ "$body" != *"$want_substr"* ]] && ok=0
  if [[ "$ok" == "1" ]]; then
    echo -e "${GREEN}[PASS]${NC} $label  (HTTP $status, body contains \"$want_substr\")"
    ((pass++)) || true
  else
    echo -e "${RED}[FAIL]${NC} $label  (got HTTP $status, body: $body)"
    ((fail++)) || true
  fi
}

post_events() {
  local data="$1"
  shift
  curl -s -o /dev/null -w "%{http_code}" \
    -X POST "$BASE/events" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $TOKEN" \
    -d "$data" \
    "$@"
}

echo "Testing $BASE/events  (token: ${TOKEN:0:4}...)"
echo "---"

# ---- Auth ----

assert_status "no auth → 401" "401" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -d '{"type":"response","service":"svc","status":200,"duration_ms":1}'

assert_status "wrong token → 401" "401" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer totally-wrong-token" \
  -d '{"type":"response","service":"svc","status":200,"duration_ms":1}'

assert_status "valid token → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"response","service":"svc","status":200,"duration_ms":1}'

# ---- JSON validation ----

assert_body "invalid JSON → 400 with error" "400" "errors" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{not valid json'

assert_body "missing type → 400 field:type" "400" '"type"' \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"service":"svc","status":200,"duration_ms":1}'

assert_body "unknown type → 400" "400" "errors" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"bogus","service":"svc","status":200,"duration_ms":1}'

# ---- Response event validation ----

assert_body "response: missing service → 400 field:service" "400" '"service"' \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"response","status":200,"duration_ms":1}'

assert_status "response: missing status → 400" "400" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"response","service":"svc","duration_ms":1}'

assert_status "response: missing duration_ms → 400" "400" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"response","service":"svc","status":200}'

# ---- Valid response events ----

assert_status "response: minimal valid → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"response","service":"auth","status":200,"duration_ms":12}'

assert_status "response: with correlation_id → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "type":"response",
    "service":"users",
    "correlation_id":"corr-test-1",
    "status":201,
    "duration_ms":38,
    "headers":{"content-type":["application/json"]},
    "body":"{\"id\":42}"
  }'

assert_status "response: explicit timestamp → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"response","service":"jobs","status":204,"duration_ms":5,"timestamp":"2026-05-19T09:00:00Z"}'

assert_status "response: correlation via traceparent header → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "type":"response",
    "service":"gateway",
    "status":200,
    "duration_ms":21,
    "headers":{"traceparent":["00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"]}
  }'

assert_status "response: correlation via X-Request-ID header → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "type":"response",
    "service":"catalog",
    "status":200,
    "duration_ms":9,
    "headers":{"X-Request-ID":["req-abc-999"]}
  }'

assert_status "response: 5xx status → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"response","service":"payments","status":503,"duration_ms":1500,"body":"upstream timeout"}'

# ---- Outbound event validation ----

assert_status "outbound: missing service → 400" "400" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"outbound","request":{"method":"GET","path":"/"},"duration_ms":1}'

assert_status "outbound: missing request.method → 400" "400" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"outbound","service":"svc","request":{"path":"/"},"duration_ms":1}'

assert_status "outbound: missing request.path → 400" "400" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"outbound","service":"svc","request":{"method":"POST"},"duration_ms":1}'

assert_status "outbound: response present without status → 400" "400" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{"type":"outbound","service":"svc","request":{"method":"GET","path":"/"},"response":{},"duration_ms":1}'

# ---- Valid outbound events ----

assert_status "outbound: with full response → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "type":"outbound",
    "service":"payments",
    "correlation_id":"corr-out-1",
    "request":{"method":"POST","path":"/charge","headers":{"content-type":["application/json"]},"body":"{\"amount\":1000}"},
    "response":{"status":201,"headers":{"content-type":["application/json"]},"body":"{\"charge_id\":\"ch_123\"}"},
    "duration_ms":145
  }'

assert_status "outbound: null response (timed out) → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "type":"outbound",
    "service":"notifications",
    "correlation_id":"corr-timeout-1",
    "request":{"method":"POST","path":"/send","body":"hello"},
    "response":null,
    "duration_ms":5000
  }'

assert_status "outbound: GET with no body → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "type":"outbound",
    "service":"catalog",
    "request":{"method":"GET","path":"/products?page=1"},
    "response":{"status":200},
    "duration_ms":33
  }'

# ---- Batch ----

assert_status "batch: two valid events → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '[
    {"type":"response","service":"svc-a","correlation_id":"batch-c1","status":200,"duration_ms":10},
    {"type":"outbound","service":"svc-b","correlation_id":"batch-c2","request":{"method":"GET","path":"/ping"},"duration_ms":5}
  ]'

assert_body "batch: empty array → 400" "400" "errors" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '[]'

assert_body "batch: invalid event at index 1 → 400 with index" "400" '"index"' \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '[
    {"type":"response","service":"svc","correlation_id":"c1","status":200,"duration_ms":1},
    {"type":"response","correlation_id":"c2","duration_ms":2}
  ]'

assert_status "batch: five response events → 202" "202" \
  -X POST "$BASE/events" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '[
    {"type":"response","service":"svc","status":200,"duration_ms":11},
    {"type":"response","service":"svc","status":201,"duration_ms":22},
    {"type":"response","service":"svc","status":204,"duration_ms":8},
    {"type":"response","service":"svc","status":400,"duration_ms":5},
    {"type":"response","service":"svc","status":500,"duration_ms":2}
  ]'

# ---- Healthcheck (sanity) ----

assert_status "GET /healthz → 200" "200" \
  "$BASE/healthz"

echo "---"
echo -e "Results: ${GREEN}${pass} passed${NC}, ${RED}${fail} failed${NC}"
