#!/usr/bin/env bash
# Sends a variety of fake HTTP requests to the httpcatch capture port.
# Usage: ./scripts/inject-requests.sh [CAPTURE_HOST] [CAPTURE_PORT]
# Set HTTPCATCH_TOKEN + ADMIN_PORT (default 8081) to also inject correlated events.
set -euo pipefail

HOST="${1:-localhost}"
PORT="${2:-8080}"
BASE="http://${HOST}:${PORT}"

ADMIN_PORT="${ADMIN_PORT:-8081}"
ADMIN_BASE="http://${HOST}:${ADMIN_PORT}"
TOKEN="${HTTPCATCH_TOKEN:-}"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

pass=0
fail=0

send() {
  local label="$1"; shift
  local status
  status=$(curl -s -o /dev/null -w "%{http_code}" "$@")
  if [[ "$status" == "202" ]]; then
    echo -e "${GREEN}[PASS]${NC} $label  (HTTP $status)"
    ((pass++)) || true
  else
    echo -e "${RED}[FAIL]${NC} $label  (HTTP $status, expected 202)"
    ((fail++)) || true
  fi
}

echo "Injecting requests to $BASE"
echo "---"

# --- GET requests ---

send "GET /" \
  -X GET "$BASE/"

send "GET /api/users with query string" \
  -X GET "$BASE/api/users?page=2&limit=50"

send "GET /health with User-Agent" \
  -X GET "$BASE/health" \
  -H "User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36"

send "GET /api/orders with X-Forwarded-* headers" \
  -X GET "$BASE/api/orders/42" \
  -H "X-Forwarded-For: 203.0.113.42, 10.0.0.1" \
  -H "X-Forwarded-Proto: https" \
  -H "X-Forwarded-Host: api.example.com" \
  -H "User-Agent: Go-http-client/1.1" \
  -H "X-Httpcatch-Service: orders-service"

send "GET with traceparent correlation" \
  -X GET "$BASE/api/trace" \
  -H "traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" \
  -H "X-Httpcatch-Service: frontend"

send "GET with X-Request-ID correlation" \
  -X GET "$BASE/api/items" \
  -H "X-Request-ID: req-$(uuidgen 2>/dev/null || echo abc123)" \
  -H "X-Httpcatch-Service: inventory"

# --- POST with JSON body ---

send "POST JSON payload" \
  -X POST "$BASE/api/users" \
  -H "Content-Type: application/json" \
  -H "User-Agent: axios/1.4.0" \
  -H "X-Httpcatch-Service: user-service" \
  -d '{"name":"Alice","email":"alice@example.com","role":"admin"}'

send "POST JSON with X-Forwarded-For" \
  -X POST "$BASE/api/payments" \
  -H "Content-Type: application/json" \
  -H "X-Forwarded-For: 198.51.100.7" \
  -H "X-Forwarded-Proto: https" \
  -H "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.dGVzdA.placeholder" \
  -H "X-Httpcatch-Service: payments" \
  -d '{"amount":9900,"currency":"EUR","card_last4":"4242"}'

send "POST JSON batch" \
  -X POST "$BASE/api/events/bulk" \
  -H "Content-Type: application/json" \
  -H "X-Httpcatch-Service: events-service" \
  -d '[{"event":"click","element":"btn-signup"},{"event":"pageview","path":"/pricing"}]'

send "POST JSON nested object" \
  -X POST "$BASE/api/orders" \
  -H "Content-Type: application/json" \
  -H "User-Agent: okhttp/4.9.0" \
  -H "X-Httpcatch-Service: checkout" \
  -d '{"customer":{"id":123,"tier":"gold"},"items":[{"sku":"ABC-001","qty":2}],"shipping":{"method":"express"}}'

# --- POST with XML body ---

send "POST XML payload" \
  -X POST "$BASE/api/feed" \
  -H "Content-Type: application/xml" \
  -H "User-Agent: Java/17 HttpClient" \
  -H "X-Httpcatch-Service: feed-ingester" \
  -d '<?xml version="1.0" encoding="UTF-8"?><feed><entry><id>1</id><title>Hello World</title></entry></feed>'

send "POST SOAP-like XML" \
  -X POST "$BASE/api/legacy/rpc" \
  -H "Content-Type: text/xml; charset=utf-8" \
  -H "SOAPAction: \"http://example.com/GetUser\"" \
  -H "X-Forwarded-For: 10.10.0.55" \
  -H "X-Httpcatch-Service: legacy-gateway" \
  -d '<?xml version="1.0"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><GetUser><UserId>42</UserId></GetUser></soap:Body></soap:Envelope>'

# --- POST with form body ---

send "POST form-urlencoded" \
  -X POST "$BASE/auth/login" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -H "User-Agent: Mozilla/5.0 Firefox/115.0" \
  -H "Origin: https://app.example.com" \
  -H "Referer: https://app.example.com/login" \
  --data-urlencode "username=bob" \
  --data-urlencode "password=hunter2" \
  --data-urlencode "remember=true"

# --- PUT ---

send "PUT JSON update" \
  -X PUT "$BASE/api/users/99" \
  -H "Content-Type: application/json" \
  -H "X-Httpcatch-Service: user-service" \
  -d '{"name":"Bob Updated","role":"viewer"}'

send "PUT with X-Forwarded-* and traceparent" \
  -X PUT "$BASE/api/products/sku-007" \
  -H "Content-Type: application/json" \
  -H "X-Forwarded-For: 203.0.113.1" \
  -H "X-Forwarded-Proto: https" \
  -H "traceparent: 00-1234567890abcdef1234567890abcdef-fedcba0987654321-01" \
  -H "X-Httpcatch-Service: catalog" \
  -d '{"price":1999,"stock":42}'

# --- PATCH ---

send "PATCH partial update" \
  -X PATCH "$BASE/api/settings/notifications" \
  -H "Content-Type: application/json" \
  -H "User-Agent: fetch/1.0" \
  -H "X-Httpcatch-Service: settings-service" \
  -d '{"email_notifications":false}'

# --- DELETE ---

send "DELETE resource" \
  -X DELETE "$BASE/api/sessions/sess-abc123" \
  -H "X-Httpcatch-Service: auth-service" \
  -H "X-Request-ID: del-$(date +%s)"

send "DELETE with X-Forwarded headers" \
  -X DELETE "$BASE/api/cache/invalidate" \
  -H "X-Forwarded-For: 10.0.1.100" \
  -H "X-Forwarded-Proto: https" \
  -H "X-Httpcatch-Service: cache-manager"

# --- Unusual but valid scenarios ---

send "POST plain text body" \
  -X POST "$BASE/api/logs" \
  -H "Content-Type: text/plain" \
  -H "X-Httpcatch-Service: log-ingestor" \
  -d "2026-05-19T10:00:00Z ERROR connection refused to db:5432"

send "GET many cookies" \
  -X GET "$BASE/dashboard" \
  -H "Cookie: session=abc123; theme=dark; locale=en-GB; csrf=xyz789" \
  -H "User-Agent: Mozilla/5.0 Chrome/120.0" \
  -H "X-Httpcatch-Service: web"

send "POST with large X-Forwarded-For chain" \
  -X POST "$BASE/api/analytics" \
  -H "Content-Type: application/json" \
  -H "X-Forwarded-For: 2001:db8::1, 198.51.100.0, 10.0.0.5, 172.16.0.1" \
  -H "X-Forwarded-Proto: https" \
  -H "X-Forwarded-Host: analytics.example.com:443" \
  -H "X-Httpcatch-Service: analytics" \
  -d '{"event":"conversion","value":149.99}'

# --- Correlated pairs (capture request + matching event) ---
# Requires HTTPCATCH_TOKEN to be set; skipped otherwise.

gen_id() {
  uuidgen 2>/dev/null || cat /proc/sys/kernel/random/uuid 2>/dev/null || printf '%08x-%04x-%04x-%04x-%012x' \
    $RANDOM $RANDOM $RANDOM $RANDOM $RANDOM
}

send_event() {
  local label="$1" data="$2"
  local status
  status=$(curl -s -o /dev/null -w "%{http_code}" \
    -X POST "$ADMIN_BASE/events" \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $TOKEN" \
    -d "$data")
  if [[ "$status" == "202" ]]; then
    echo -e "  ${GREEN}[EVENT OK]${NC} $label  (HTTP $status)"
    ((pass++)) || true
  else
    echo -e "  ${RED}[EVENT FAIL]${NC} $label  (HTTP $status)"
    ((fail++)) || true
  fi
}

if [[ -z "$TOKEN" ]]; then
  echo -e "${YELLOW}[SKIP]${NC} Correlated pairs — set HTTPCATCH_TOKEN to enable"
else
  echo "--- Correlated pairs ---"

  # Pair 1: GET /api/profile → service responds 200
  CORR_1=$(gen_id)
  send "GET /api/profile  [corr: ${CORR_1:0:8}...]" \
    -X GET "$BASE/api/profile/me" \
    -H "X-Request-ID: $CORR_1" \
    -H "X-Httpcatch-Service: profile-service" \
    -H "User-Agent: Mozilla/5.0 Chrome/120.0" \
    -H "Accept: application/json"
  sleep 0.1
  send_event "response event for GET /api/profile" \
    "{\"type\":\"response\",\"service\":\"profile-service\",\"correlation_id\":\"$CORR_1\",\"status\":200,\"duration_ms\":18,\"headers\":{\"content-type\":[\"application/json\"]},\"body\":\"{\\\"id\\\":\\\"me\\\",\\\"name\\\":\\\"Alice\\\"}\"}"

  # Pair 2: POST /api/checkout → service calls payment provider (outbound)
  CORR_2=$(gen_id)
  send "POST /api/checkout  [corr: ${CORR_2:0:8}...]" \
    -X POST "$BASE/api/checkout" \
    -H "X-Request-ID: $CORR_2" \
    -H "X-Httpcatch-Service: checkout-service" \
    -H "Content-Type: application/json" \
    -H "X-Forwarded-For: 203.0.113.5" \
    -H "X-Forwarded-Proto: https" \
    -d '{"cart_id":"cart-999","promo":"SAVE10"}'
  sleep 0.1
  send_event "response event for POST /api/checkout" \
    "{\"type\":\"response\",\"service\":\"checkout-service\",\"correlation_id\":\"$CORR_2\",\"status\":201,\"duration_ms\":342,\"body\":\"{\\\"order_id\\\":\\\"ord-555\\\"}\"}"
  send_event "outbound event (checkout → payment provider)" \
    "{\"type\":\"outbound\",\"service\":\"checkout-service\",\"correlation_id\":\"$CORR_2\",\"request\":{\"method\":\"POST\",\"path\":\"/v1/charges\",\"headers\":{\"content-type\":[\"application/json\"]},\"body\":\"{\\\"amount\\\":8910}\"},\"response\":{\"status\":200,\"body\":\"{\\\"charge_id\\\":\\\"ch_abc\\\"\"}},\"duration_ms\":198}"

  # Pair 3: DELETE /api/sessions → service responds 204, also calls audit log (outbound, null response = timed out)
  CORR_3=$(gen_id)
  send "DELETE /api/sessions/sess-xyz  [corr: ${CORR_3:0:8}...]" \
    -X DELETE "$BASE/api/sessions/sess-xyz" \
    -H "X-Request-ID: $CORR_3" \
    -H "X-Httpcatch-Service: auth-service" \
    -H "User-Agent: okhttp/4.9.0"
  sleep 0.1
  send_event "response event for DELETE /api/sessions" \
    "{\"type\":\"response\",\"service\":\"auth-service\",\"correlation_id\":\"$CORR_3\",\"status\":204,\"duration_ms\":9}"
  send_event "outbound event (auth → audit log, timed out)" \
    "{\"type\":\"outbound\",\"service\":\"auth-service\",\"correlation_id\":\"$CORR_3\",\"request\":{\"method\":\"POST\",\"path\":\"/audit/events\",\"body\":\"{\\\"action\\\":\\\"logout\\\"}\"},\"response\":null,\"duration_ms\":5000}"
fi

echo "---"
echo -e "Results: ${GREEN}${pass} passed${NC}, ${RED}${fail} failed${NC}"
