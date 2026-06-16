package admin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/sinks"
)

const (
	defaultBodyCap   = 1 << 20 // 1 MiB
	defaultMaxEvents = 1 << 20 // 1 MiB
)

// newEventsServer builds a test server with the given queue, body cap, and max payload.
// If readers is non-nil it is also wired so tests can read back enqueued events.
func newEventsServer(t *testing.T, queue *capture.Queue, bodyCap, maxPayload int, readers ...admin.ReadSources) (*httptest.Server, *admin.EventsCounters) {
	t.Helper()
	return newEventsServerFull(t, queue, bodyCap, maxPayload, 0, readers...)
}

// newEventsServerFull builds a test server with the full set of configurable limits.
func newEventsServerFull(t *testing.T, queue *capture.Queue, bodyCap, maxPayload, maxBatch int, readers ...admin.ReadSources) (*httptest.Server, *admin.EventsCounters) {
	t.Helper()
	cfg := config.AdminConfig{
		Bind:       "127.0.0.1:0",
		Token:      testAdminToken,
		SessionTTL: time.Hour,
	}
	counters := admin.NewEventsCounters()
	es := admin.EventsSources{
		Queue:             queue,
		BodyCap:           bodyCap,
		MaxEventsPayload:  maxPayload,
		MaxEventsPerBatch: maxBatch,
		Counters:          counters,
	}
	var rs admin.ReadSources
	if len(readers) > 0 {
		rs = readers[0]
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{}, admin.ServerOptions{
		Readers: rs,
		Events:  es,
	})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts, counters
}

// postEvents sends a POST /events request with the given body.
func postEvents(t *testing.T, ts *httptest.Server, body string, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /events: %v", err)
	}
	return resp
}

// drainQueue reads n records from the queue's receive channel into ss and returns them.
func drainQueue(t *testing.T, q *capture.Queue, n int) []capture.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	recs := make([]capture.Record, 0, n)
	ch := q.Receive()
	for len(recs) < n {
		select {
		case r, ok := <-ch:
			if !ok {
				t.Fatalf("queue closed before reading %d records (got %d)", n, len(recs))
			}
			recs = append(recs, r)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %d records (got %d)", n, len(recs))
		}
	}
	return recs
}

// ---- Auth tests ----

func TestEvents_NoBearerAuth_Returns401(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{"type":"response","service":"svc","status":200,"duration_ms":1}`, "")
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no auth: got %d want 401", resp.StatusCode)
	}
}

func TestEvents_CookieAuth_Returns401(t *testing.T) {
	t.Parallel()

	// Cookie auth is explicitly disabled on POST /events to prevent CSRF.
	q := capture.NewQueue(10)
	cfg := config.AdminConfig{
		Bind:       "127.0.0.1:0",
		Token:      testAdminToken,
		SessionTTL: time.Hour,
	}
	counters := admin.NewEventsCounters()
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{}, admin.ServerOptions{
		Events: admin.EventsSources{
			Queue:            q,
			BodyCap:          defaultBodyCap,
			MaxEventsPayload: defaultMaxEvents,
			Counters:         counters,
		},
	})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	// Create a valid session by logging in. Use a no-redirect client so the
	// Set-Cookie from the 303 response is captured before the redirect is followed.
	client := noFollowClient()
	form := "token=" + testAdminToken
	loginReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/auth/login",
		strings.NewReader(form))
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResp, err2 := client.Do(loginReq)
	if err2 != nil {
		t.Fatalf("POST /auth/login: %v", err2)
	}
	var sessionCookie *http.Cookie
	for _, c := range loginResp.Cookies() {
		if c.Name == "httpcatch_session" {
			sessionCookie = c
		}
	}
	io.Copy(io.Discard, loginResp.Body)
	loginResp.Body.Close()
	if sessionCookie == nil {
		t.Fatal("no session cookie after login")
	}

	// POST /events with a cookie (no bearer) must return 401.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/events",
		strings.NewReader(`{"type":"response","service":"svc","status":200,"duration_ms":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(sessionCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /events with cookie: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("cookie-auth on /events: got %d want 401", resp.StatusCode)
	}
}

func TestEvents_ValidBearer_Returns202(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts, `{"type":"response","service":"svc","status":200,"duration_ms":1}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("valid bearer: got %d want 202", resp.StatusCode)
	}
}

// ---- Validation: type field ----

func TestEvents_MissingType_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts, `{"service":"svc","status":200,"duration_ms":1}`, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing type: got %d want 400", resp.StatusCode)
	}
	var body struct {
		Errors []map[string]any `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Errors) == 0 {
		t.Fatal("expected errors in body")
	}
	if body.Errors[0]["field"] != "type" {
		t.Errorf("field: got %v want type", body.Errors[0]["field"])
	}
	if counters.EventsRejectedMissingTypeTotal() != 1 {
		t.Errorf("missing_type counter: got %d want 1", counters.EventsRejectedMissingTypeTotal())
	}
}

func TestEvents_UnknownType_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts, `{"type":"unknown_thing","service":"svc","status":200,"duration_ms":1}`, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown type: got %d want 400", resp.StatusCode)
	}
	if counters.EventsRejectedUnknownTypeTotal() != 1 {
		t.Errorf("unknown_type counter: got %d want 1", counters.EventsRejectedUnknownTypeTotal())
	}
}

func TestEvents_InvalidJSON_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts, `{not valid json`, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid json: got %d want 400", resp.StatusCode)
	}
	if counters.EventsRejectedInvalidJSONTotal() != 1 {
		t.Errorf("invalid_json counter: got %d want 1", counters.EventsRejectedInvalidJSONTotal())
	}
}

// ---- Validation: response event required fields ----

func TestEvents_ResponseEvent_MissingService_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts, `{"type":"response","status":200,"duration_ms":1}`, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing service: got %d want 400", resp.StatusCode)
	}
	var body struct {
		Errors []map[string]any `json:"errors"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	found := false
	for _, e := range body.Errors {
		if e["field"] == "service" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected service error, got %v", body.Errors)
	}
	if counters.EventsRejectedMissingRequiredFieldTotal() != 1 {
		t.Errorf("missing_required_field counter: got %d want 1", counters.EventsRejectedMissingRequiredFieldTotal())
	}
}

func TestEvents_ResponseEvent_InvalidService_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts, `{"type":"response","service":"bad\u0001svc","status":200,"duration_ms":1}`, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid service: got %d want 400", resp.StatusCode)
	}
	if counters.EventsRejectedMissingRequiredFieldTotal() != 1 {
		t.Errorf("missing_required_field counter: got %d want 1", counters.EventsRejectedMissingRequiredFieldTotal())
	}
}

func TestEvents_ResponseEvent_ServiceNormalised(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts, `{"type":"response","service":" Billing-API ","status":200,"duration_ms":1}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re, ok := recs[0].(*capture.ResponseEvent)
	if !ok {
		t.Fatalf("record is %T, want *capture.ResponseEvent", recs[0])
	}
	if re.Service != "billing-api" {
		t.Errorf("service: got %q want billing-api", re.Service)
	}
}

func TestEvents_ResponseEvent_MissingStatus_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts, `{"type":"response","service":"svc","duration_ms":1}`, testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing status: got %d want 400", resp.StatusCode)
	}
}

func TestEvents_ResponseEvent_MissingDurationMS_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts, `{"type":"response","service":"svc","status":200}`, testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing duration_ms: got %d want 400", resp.StatusCode)
	}
}

// ---- Validation: outbound event required fields ----

func TestEvents_OutboundEvent_MissingService_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts,
		`{"type":"outbound","request":{"method":"GET","path":"/"},"duration_ms":1}`,
		testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing service: got %d want 400", resp.StatusCode)
	}
}

func TestEvents_OutboundEvent_MissingRequestMethod_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts,
		`{"type":"outbound","service":"svc","request":{"path":"/"},"duration_ms":1}`,
		testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing request.method: got %d want 400", resp.StatusCode)
	}
}

func TestEvents_OutboundEvent_MissingRequestPath_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts,
		`{"type":"outbound","service":"svc","request":{"method":"POST"},"duration_ms":1}`,
		testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing request.path: got %d want 400", resp.StatusCode)
	}
}

func TestEvents_OutboundEvent_ResponsePresentMissingStatus_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts,
		`{"type":"outbound","service":"svc","request":{"method":"GET","path":"/"},"response":{},"duration_ms":1}`,
		testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("outbound with response missing status: got %d want 400", resp.StatusCode)
	}
}

// ---- Single valid events ----

func TestEvents_SingleResponseEvent_Enqueued(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"users",
		"correlation_id":"corr-1",
		"status":200,
		"duration_ms":42,
		"headers":{"content-type":["application/json"]},
		"body":"hello"
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re, ok := recs[0].(*capture.ResponseEvent)
	if !ok {
		t.Fatalf("record is %T, want *capture.ResponseEvent", recs[0])
	}
	if re.Service != "users" {
		t.Errorf("service: got %q want users", re.Service)
	}
	if re.CorrelationID != "corr-1" {
		t.Errorf("correlation_id: got %q want corr-1", re.CorrelationID)
	}
	if re.CorrelationSource != capture.CorrelationSourceExplicit {
		t.Errorf("correlation_source: got %q want explicit", re.CorrelationSource)
	}
	if re.Status != 200 {
		t.Errorf("status: got %d want 200", re.Status)
	}
	if re.DurationMS != 42 {
		t.Errorf("duration_ms: got %d want 42", re.DurationMS)
	}
	if counters.EventsIngestedResponseTotal() != 1 {
		t.Errorf("ingested_response counter: got %d want 1", counters.EventsIngestedResponseTotal())
	}
}

func TestEvents_SingleOutboundEvent_Enqueued(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"outbound",
		"service":"payments",
		"correlation_id":"corr-2",
		"request":{"method":"POST","path":"/charge","headers":{},"body":"req-body"},
		"response":{"status":201,"headers":{},"body":"resp-body"},
		"duration_ms":38
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	oe, ok := recs[0].(*capture.OutboundEvent)
	if !ok {
		t.Fatalf("record is %T, want *capture.OutboundEvent", recs[0])
	}
	if oe.Service != "payments" {
		t.Errorf("service: got %q want payments", oe.Service)
	}
	if oe.Request.Method != "POST" {
		t.Errorf("request.method: got %q want POST", oe.Request.Method)
	}
	if oe.Request.Path != "/charge" {
		t.Errorf("request.path: got %q want /charge", oe.Request.Path)
	}
	if oe.Response == nil {
		t.Fatal("response is nil")
	}
	if oe.Response.Status != 201 {
		t.Errorf("response.status: got %d want 201", oe.Response.Status)
	}
	if counters.EventsIngestedOutboundTotal() != 1 {
		t.Errorf("ingested_outbound counter: got %d want 1", counters.EventsIngestedOutboundTotal())
	}
}

func TestEvents_OutboundEvent_NullResponse_Accepted(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"outbound",
		"service":"jobs",
		"correlation_id":"corr-3",
		"request":{"method":"GET","path":"/status"},
		"response":null,
		"duration_ms":5
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("outbound null response: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	oe, ok := recs[0].(*capture.OutboundEvent)
	if !ok {
		t.Fatalf("record is %T, want *capture.OutboundEvent", recs[0])
	}
	if oe.Response != nil {
		t.Errorf("response: expected nil, got %+v", oe.Response)
	}
}

// ---- Batch ----

func TestEvents_Batch_TwoValidEvents_BothEnqueued(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	body := `[
		{"type":"response","service":"svc","correlation_id":"c1","status":200,"duration_ms":1},
		{"type":"outbound","service":"svc","correlation_id":"c2","request":{"method":"GET","path":"/"},"duration_ms":2}
	]`
	resp := postEvents(t, ts, body, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("batch: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 2)
	if len(recs) != 2 {
		t.Fatalf("expected 2 records, got %d", len(recs))
	}
	if _, ok := recs[0].(*capture.ResponseEvent); !ok {
		t.Errorf("recs[0]: got %T want *capture.ResponseEvent", recs[0])
	}
	if _, ok := recs[1].(*capture.OutboundEvent); !ok {
		t.Errorf("recs[1]: got %T want *capture.OutboundEvent", recs[1])
	}
	if counters.EventsIngestedResponseTotal() != 1 {
		t.Errorf("ingested_response: got %d want 1", counters.EventsIngestedResponseTotal())
	}
	if counters.EventsIngestedOutboundTotal() != 1 {
		t.Errorf("ingested_outbound: got %d want 1", counters.EventsIngestedOutboundTotal())
	}
}

func TestEvents_Batch_InvalidAtIndex1_NothingEnqueued(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	// First event is valid; second is missing required fields.
	body := `[
		{"type":"response","service":"svc","correlation_id":"c1","status":200,"duration_ms":1},
		{"type":"response","correlation_id":"c2","duration_ms":2}
	]`
	resp := postEvents(t, ts, body, testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("batch with invalid at index 1: got %d want 400", resp.StatusCode)
	}

	var errBody struct {
		Errors []map[string]any `json:"errors"`
	}
	json.NewDecoder(resp.Body).Decode(&errBody)
	if len(errBody.Errors) == 0 {
		t.Fatal("expected errors in body")
	}
	// Index should be 1.
	idx := errBody.Errors[0]["index"]
	if idx != float64(1) {
		t.Errorf("error index: got %v want 1", idx)
	}
	// Nothing was enqueued.
	if counters.EventsIngestedResponseTotal() != 0 {
		t.Errorf("ingested after batch rejection: got %d want 0", counters.EventsIngestedResponseTotal())
	}
	if counters.EventsRejectedMissingRequiredFieldTotal() != 1 {
		t.Errorf("missing_required_field counter: got %d want 1", counters.EventsRejectedMissingRequiredFieldTotal())
	}
}

func TestEvents_EmptyBatch_Returns400(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)
	resp := postEvents(t, ts, `[]`, testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty batch: got %d want 400", resp.StatusCode)
	}
	if counters.EventsRejectedEmptyBatchTotal() != 1 {
		t.Errorf("empty_batch counter: got %d want 1", counters.EventsRejectedEmptyBatchTotal())
	}
}

// ---- Payload size cap ----

func TestEvents_PayloadExceedsCap_Returns413(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	// Set max payload to 100 bytes.
	ts, counters := newEventsServer(t, q, defaultBodyCap, 100)

	// Build a body larger than 100 bytes.
	largeBody := `{"type":"response","service":"svc","status":200,"duration_ms":1,"body":"` + strings.Repeat("x", 200) + `"}`
	resp := postEvents(t, ts, largeBody, testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("payload too large: got %d want 413", resp.StatusCode)
	}
	if counters.EventsRejectedPayloadTooLargeTotal() != 1 {
		t.Errorf("payload_too_large counter: got %d want 1", counters.EventsRejectedPayloadTooLargeTotal())
	}
}

// ---- Per-event body cap ----

func TestEvents_BodyExceedsBodyCap_Rejected(t *testing.T) {
	t.Parallel()

	// When bodyCap > 0, a body exceeding the cap is rejected with 400.
	bodyCap := 5
	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, bodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"correlation_id":"c1",
		"status":200,
		"duration_ms":1,
		"body":"this body is definitely longer than five bytes"
	}`, testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("body over cap: got %d want 400", resp.StatusCode)
	}
	if counters.EventsRejectedBodyTooLargeTotal() != 1 {
		t.Errorf("body_too_large counter: got %d want 1", counters.EventsRejectedBodyTooLargeTotal())
	}
}

func TestEvents_OutboundBodyCap_Rejected(t *testing.T) {
	t.Parallel()

	// When bodyCap > 0, outbound bodies exceeding the cap are rejected.
	bodyCap := 3
	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, bodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"outbound",
		"service":"svc",
		"correlation_id":"c1",
		"request":{"method":"GET","path":"/","body":"request body here"},
		"response":{"status":200,"body":"response body here"},
		"duration_ms":1
	}`, testAdminToken)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("outbound body over cap: got %d want 400", resp.StatusCode)
	}
	if counters.EventsRejectedBodyTooLargeTotal() != 1 {
		t.Errorf("body_too_large counter: got %d want 1", counters.EventsRejectedBodyTooLargeTotal())
	}
}

func TestEvents_BodyWithinCap_Accepted(t *testing.T) {
	t.Parallel()

	// A body exactly at the cap should be accepted and stored without truncation.
	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, 5, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"status":200,
		"duration_ms":1,
		"body":"hello"
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("body at cap: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.BodyTruncated {
		t.Error("body_truncated: expected false for body at exactly cap")
	}
}

// ---- Correlation derivation ----

func TestEvents_CorrelationFromTraceparent(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"status":200,
		"duration_ms":1,
		"headers":{"traceparent":["00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"]}
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.CorrelationSource != capture.CorrelationSourceTraceparent {
		t.Errorf("correlation_source: got %q want traceparent", re.CorrelationSource)
	}
	if re.CorrelationID != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("correlation_id: got %q want aaaa...", re.CorrelationID)
	}
}

func TestEvents_CorrelationFromXRequestID(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"status":200,
		"duration_ms":1,
		"headers":{"X-Request-ID":["my-request-id-xyz"]}
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.CorrelationSource != capture.CorrelationSourceRequestID {
		t.Errorf("correlation_source: got %q want %q", re.CorrelationSource, capture.CorrelationSourceRequestID)
	}
	if re.CorrelationID != "my-request-id-xyz" {
		t.Errorf("correlation_id: got %q want my-request-id-xyz", re.CorrelationID)
	}
}

func TestEvents_CorrelationSynthesized(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"status":200,
		"duration_ms":1
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.CorrelationSource != capture.CorrelationSourceSynthesized {
		t.Errorf("correlation_source: got %q want synthesized", re.CorrelationSource)
	}
	if re.CorrelationID == "" {
		t.Error("correlation_id must be non-empty even when synthesized")
	}
}

func TestEvents_ExplicitCorrelationOverridesHeaders(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"correlation_id":"explicit-id",
		"status":200,
		"duration_ms":1,
		"headers":{"traceparent":["00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"]}
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.CorrelationSource != capture.CorrelationSourceExplicit {
		t.Errorf("correlation_source: got %q want explicit", re.CorrelationSource)
	}
	if re.CorrelationID != "explicit-id" {
		t.Errorf("correlation_id: got %q want explicit-id", re.CorrelationID)
	}
}

// ---- Timestamp ----

func TestEvents_TimestampAbsent_UsesServerTime(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	before := time.Now()
	resp := postEvents(t, ts, `{"type":"response","service":"svc","status":200,"duration_ms":1}`, testAdminToken)
	after := time.Now()
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.Timestamp.Before(before) || re.Timestamp.After(after) {
		t.Errorf("timestamp %v not in [%v, %v]", re.Timestamp, before, after)
	}
}

func TestEvents_ExplicitTimestamp_Used(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	wantTS := "2026-05-18T10:00:00Z"
	body := fmt.Sprintf(`{"type":"response","service":"svc","status":200,"duration_ms":1,"timestamp":"%s"}`, wantTS)
	resp := postEvents(t, ts, body, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	want, _ := time.Parse(time.RFC3339, wantTS)
	if !re.Timestamp.Equal(want) {
		t.Errorf("timestamp: got %v want %v", re.Timestamp, want)
	}
}

// ---- Counters ----

func TestEvents_Counters_AdvanceOnSuccess(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	// Enqueue 2 response events.
	postEvents(t, ts, `{"type":"response","service":"svc","status":200,"duration_ms":1}`, testAdminToken)
	postEvents(t, ts, `{"type":"response","service":"svc","status":201,"duration_ms":2}`, testAdminToken)
	drainQueue(t, q, 2)

	if counters.EventsIngestedResponseTotal() != 2 {
		t.Errorf("ingested_response: got %d want 2", counters.EventsIngestedResponseTotal())
	}
}

// ---- Metrics endpoint reflects events counters ----

func TestMetrics_EventsCountersPresent(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	counters := admin.NewEventsCounters()
	cfg := config.AdminConfig{
		Bind:       "127.0.0.1:0",
		Token:      testAdminToken,
		SessionTTL: time.Hour,
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{
		EventsIngestedResponseTotal: counters.EventsIngestedResponseTotal,
		EventsIngestedOutboundTotal: counters.EventsIngestedOutboundTotal,
	}, admin.ServerOptions{
		Events: admin.EventsSources{
			Queue:            q,
			BodyCap:          defaultBodyCap,
			MaxEventsPayload: defaultMaxEvents,
			Counters:         counters,
		},
	})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	// Post one event.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/events",
		strings.NewReader(`{"type":"response","service":"svc","status":200,"duration_ms":1}`))
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	resp, _ := http.DefaultClient.Do(req)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	drainQueue(t, q, 1)

	// Fetch /metrics.
	mResp, _ := http.Get(ts.URL + "/metrics")
	body, _ := io.ReadAll(mResp.Body)
	mResp.Body.Close()

	bodyStr := string(body)
	if !strings.Contains(bodyStr, "httpcatch_events_ingested_total") {
		t.Errorf("/metrics missing httpcatch_events_ingested_total\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "httpcatch_events_rejected_total") {
		t.Errorf("/metrics missing httpcatch_events_rejected_total\n%s", bodyStr)
	}
}

// ---- Integration: event flows through pipeline into sinks ----

// drainWorker runs a single worker iteration draining n records from q through
// the redactor (no-op) into the given sinks.
func drainWorkerInto(t *testing.T, q *capture.Queue, ss []sinks.Sink, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ch := q.Receive()
	drained := 0
	for drained < n {
		select {
		case rec, ok := <-ch:
			if !ok {
				t.Fatalf("queue closed early (got %d want %d)", drained, n)
			}
			for _, s := range ss {
				if err := s.Write(ctx, rec); err != nil {
					t.Errorf("sink.Write: %v", err)
				}
			}
			drained++
		case <-ctx.Done():
			t.Fatalf("drain timeout (got %d want %d)", drained, n)
		}
	}
}

func TestEvents_Integration_EndsUpInSinks(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(100)
	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/test.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	q := capture.NewQueue(10)
	readers := admin.ReadSources{Memory: mem, SQLite: sqliteSink}
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents, readers)

	// First write a captured request that shares the correlation id.
	corrID := "corr-integration-test"
	capturedReq := &capture.CapturedRequest{
		ID:                "req-int-1",
		Timestamp:         time.Now().Add(-time.Second),
		Service:           "svc",
		Method:            "GET",
		Path:              "/api",
		CorrelationID:     corrID,
		CorrelationSource: capture.CorrelationSourceTraceparent,
		SourceIP:          "127.0.0.1",
		Headers:           map[string][]string{},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte{},
	}
	if err := mem.Write(context.Background(), capturedReq); err != nil {
		t.Fatalf("mem.Write captured request: %v", err)
	}
	if err := sqliteSink.Write(context.Background(), capturedReq); err != nil {
		t.Fatalf("sqlite.Write captured request: %v", err)
	}

	// POST a response event with the same correlation id.
	payload := fmt.Sprintf(`{"type":"response","service":"svc","correlation_id":"%s","status":200,"duration_ms":10}`, corrID)
	resp := postEvents(t, ts, payload, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /events: got %d want 202", resp.StatusCode)
	}

	// Manually drive the record through the sinks (no worker pool here).
	drainWorkerInto(t, q, []sinks.Sink{mem, sqliteSink}, 1)

	// GET /requests/{id} should now show the event in the events array.
	getResp := getRequestDetail(t, ts, "req-int-1")
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /requests/req-int-1: got %d", getResp.StatusCode)
	}
	body := decodeDetailBody(t, getResp)
	if len(body.Events) == 0 {
		t.Error("expected at least one event in detail response")
	}
}

// ---- S-EVT-01: ContentType population from wire field or headers ----

func TestEvents_ContentType_FromWireField(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"status":200,
		"duration_ms":1,
		"body":"hello",
		"content_type":"application/json"
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.ContentType != "application/json" {
		t.Errorf("ContentType: got %q, want application/json", re.ContentType)
	}
}

func TestEvents_ContentType_FallbackFromHeader(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	// No content_type wire field; Content-Type header should be used as fallback.
	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"status":200,
		"duration_ms":1,
		"body":"hello",
		"headers":{"Content-Type":["text/plain"]}
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.ContentType != "text/plain" {
		t.Errorf("ContentType: got %q, want text/plain", re.ContentType)
	}
}

func TestEvents_ContentType_EmptyWhenNeitherPresent(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"status":200,
		"duration_ms":1,
		"body":"hello"
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.ContentType != "" {
		t.Errorf("ContentType: got %q, want empty string", re.ContentType)
	}
}

func TestEvents_ContentType_WireFieldTakesPrecedenceOverHeader(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	// Wire field wins when both are present.
	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"status":200,
		"duration_ms":1,
		"content_type":"application/json",
		"headers":{"Content-Type":["text/plain"]}
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.ContentType != "application/json" {
		t.Errorf("ContentType: got %q, want application/json (wire field)", re.ContentType)
	}
}

func TestEvents_Outbound_ContentType_PopulatedOnBothHalves(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"outbound",
		"service":"svc",
		"duration_ms":5,
		"request":{
			"method":"POST",
			"path":"/api",
			"content_type":"application/json",
			"body":"{}"
		},
		"response":{
			"status":200,
			"headers":{"Content-Type":["text/html"]},
			"body":"ok"
		}
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	oe := recs[0].(*capture.OutboundEvent)
	if oe.Request.ContentType != "application/json" {
		t.Errorf("request ContentType: got %q, want application/json", oe.Request.ContentType)
	}
	// Response has no wire content_type but has a Content-Type header.
	if oe.Response.ContentType != "text/html" {
		t.Errorf("response ContentType: got %q, want text/html", oe.Response.ContentType)
	}
}

func TestEvents_Integration_OutboundWithNullResponse_InSinks(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(100)
	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/test.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents, admin.ReadSources{Memory: mem, SQLite: sqliteSink})

	resp := postEvents(t, ts, `{
		"type":"outbound",
		"service":"svc",
		"correlation_id":"null-resp-corr",
		"request":{"method":"DELETE","path":"/resource/1"},
		"response":null,
		"duration_ms":3
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST /events: got %d want 202", resp.StatusCode)
	}

	drainWorkerInto(t, q, []sinks.Sink{mem, sqliteSink}, 1)

	// Verify the event landed in the memory ring buffer.
	// ReadDetail resolves any record by its id, including OutboundEvent.
	// We don't know the id from the outside, so we use Recent to find it.
	recent := mem.Recent(mem.Len())
	var found *capture.OutboundEvent
	for _, r := range recent {
		if oe, ok := r.(*capture.OutboundEvent); ok && oe.CorrelationID == "null-resp-corr" {
			found = oe
			break
		}
	}
	if found == nil {
		t.Fatal("outbound event with null response not found in memory sink")
	}
	if found.Response != nil {
		t.Errorf("response: expected nil, got %+v", found.Response)
	}
}

// ---- Batch count cap ----

// validResponseItem returns a minimal valid response-event JSON string.
func validResponseItem(service string) string {
	return fmt.Sprintf(`{"type":"response","service":%q,"status":200,"duration_ms":1}`, service)
}

// buildBatch encodes n copies of item as a JSON array string.
func buildBatch(item string, n int) string {
	items := make([]string, n)
	for i := range items {
		items[i] = item
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestEventsHandler_Batch_AtCount_Accepted(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServerFull(t, q, defaultBodyCap, defaultMaxEvents, 3)

	body := buildBatch(validResponseItem("svc"), 3)
	resp := postEvents(t, ts, body, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("batch at cap: got %d want 202", resp.StatusCode)
	}
	if counters.EventsRejectedBatchTooLargeTotal() != 0 {
		t.Errorf("batch_too_large counter: got %d want 0", counters.EventsRejectedBatchTooLargeTotal())
	}
	drainQueue(t, q, 3)
}

func TestEventsHandler_Batch_OverCount_Rejected(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServerFull(t, q, defaultBodyCap, defaultMaxEvents, 3)

	body := buildBatch(validResponseItem("svc"), 4)
	resp := postEvents(t, ts, body, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("batch over cap: got %d want 413", resp.StatusCode)
	}

	var errBody struct {
		Errors []map[string]any `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if len(errBody.Errors) == 0 {
		t.Fatal("expected errors in body")
	}
	msg, _ := errBody.Errors[0]["message"].(string)
	if !strings.Contains(msg, "batch too large") {
		t.Errorf("error message: got %q, want to contain 'batch too large'", msg)
	}
	if counters.EventsRejectedBatchTooLargeTotal() != 1 {
		t.Errorf("batch_too_large counter: got %d want 1", counters.EventsRejectedBatchTooLargeTotal())
	}
	if counters.EventsIngestedResponseTotal() != 0 {
		t.Errorf("ingested after rejection: got %d want 0", counters.EventsIngestedResponseTotal())
	}

	// Queue must be untouched: attempt to read would time out; verify via counter.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	select {
	case _, ok := <-q.Receive():
		if ok {
			t.Error("queue received a record despite batch being rejected")
		}
	case <-ctx.Done():
		// Expected: nothing in queue.
	}
}

func TestEventsHandler_Batch_NoCountCap_AllowsLarge(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(100)
	// maxBatch = 0 disables the count cap.
	ts, counters := newEventsServerFull(t, q, defaultBodyCap, 0, 0)

	body := buildBatch(validResponseItem("svc"), 50)
	resp := postEvents(t, ts, body, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("large batch with no cap: got %d want 202", resp.StatusCode)
	}
	if counters.EventsRejectedBatchTooLargeTotal() != 0 {
		t.Errorf("batch_too_large counter: got %d want 0", counters.EventsRejectedBatchTooLargeTotal())
	}
	drainQueue(t, q, 50)
}

func TestEventsHandler_Batch_NonBatchUnaffected(t *testing.T) {
	t.Parallel()

	// maxBatch = 1 would reject any array with > 1 item, but a single-object POST
	// must pass through untouched regardless.
	q := capture.NewQueue(10)
	ts, counters := newEventsServerFull(t, q, defaultBodyCap, defaultMaxEvents, 1)

	resp := postEvents(t, ts, validResponseItem("svc"), testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("single-object POST with maxBatch=1: got %d want 202", resp.StatusCode)
	}
	if counters.EventsRejectedBatchTooLargeTotal() != 0 {
		t.Errorf("batch_too_large counter: got %d want 0", counters.EventsRejectedBatchTooLargeTotal())
	}
	drainQueue(t, q, 1)
}

// ---- Correlation ID validation ----

func TestEvents_OversizedCorrelationID_FallsThrough(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	// Build a correlation_id longer than maxExplicitCorrelationID (256 bytes).
	oversized := strings.Repeat("a", 257)
	body := fmt.Sprintf(`{"type":"response","service":"svc","correlation_id":%q,"status":200,"duration_ms":1}`, oversized)
	resp := postEvents(t, ts, body, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("oversized correlation_id: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	// Must not have stored the oversized value.
	if re.CorrelationID == oversized {
		t.Error("oversized correlation_id was stored verbatim; expected fall-through to synthesized")
	}
	if re.CorrelationSource == capture.CorrelationSourceExplicit {
		t.Errorf("correlation_source: got explicit, expected synthesized or header-derived")
	}
}

func TestEvents_ControlCharCorrelationID_FallsThrough(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, _ := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	// Embed a control character inside the correlation_id via JSON unicode escape.
	body := `{"type":"response","service":"svc","correlation_id":"bad\u0001value","status":200,"duration_ms":1}`
	resp := postEvents(t, ts, body, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("control-char correlation_id: got %d want 202", resp.StatusCode)
	}

	recs := drainQueue(t, q, 1)
	re := recs[0].(*capture.ResponseEvent)
	if re.CorrelationSource == capture.CorrelationSourceExplicit {
		t.Errorf("correlation_source: got explicit, expected synthesized (control char should be rejected)")
	}
}

// ---- Per-event body cap rejection (S-EVT-04) ----

func TestEvents_ResponseEvent_BodyTooLarge(t *testing.T) {
	t.Parallel()

	bodyCap := 10
	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, bodyCap, defaultMaxEvents)

	// Body is 11 bytes, cap is 10.
	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"status":200,
		"duration_ms":1,
		"body":"hello world"
	}`, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("body too large response event: got %d want 400", resp.StatusCode)
	}
	var body struct {
		Errors []map[string]any `json:"errors"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Errors) == 0 {
		t.Fatal("expected errors in body")
	}
	if body.Errors[0]["field"] != "body" {
		t.Errorf("error field: got %v want body", body.Errors[0]["field"])
	}
	if counters.EventsRejectedBodyTooLargeTotal() != 1 {
		t.Errorf("body_too_large counter: got %d want 1", counters.EventsRejectedBodyTooLargeTotal())
	}
	// Nothing enqueued.
	if counters.EventsIngestedResponseTotal() != 0 {
		t.Errorf("ingested after rejection: got %d want 0", counters.EventsIngestedResponseTotal())
	}
}

func TestEvents_OutboundEvent_RequestBodyTooLarge(t *testing.T) {
	t.Parallel()

	bodyCap := 5
	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, bodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"outbound",
		"service":"svc",
		"request":{"method":"POST","path":"/","body":"toolarge"},
		"duration_ms":1
	}`, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("outbound request body too large: got %d want 400", resp.StatusCode)
	}
	var body struct {
		Errors []map[string]any `json:"errors"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Errors) == 0 {
		t.Fatal("expected errors in body")
	}
	if body.Errors[0]["field"] != "request.body" {
		t.Errorf("error field: got %v want request.body", body.Errors[0]["field"])
	}
	if counters.EventsRejectedBodyTooLargeTotal() != 1 {
		t.Errorf("body_too_large counter: got %d want 1", counters.EventsRejectedBodyTooLargeTotal())
	}
}

func TestEvents_OutboundEvent_ResponseBodyTooLarge(t *testing.T) {
	t.Parallel()

	bodyCap := 5
	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, bodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"outbound",
		"service":"svc",
		"request":{"method":"GET","path":"/"},
		"response":{"status":200,"body":"toolarge"},
		"duration_ms":1
	}`, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("outbound response body too large: got %d want 400", resp.StatusCode)
	}
	var body struct {
		Errors []map[string]any `json:"errors"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Errors) == 0 {
		t.Fatal("expected errors in body")
	}
	if body.Errors[0]["field"] != "response.body" {
		t.Errorf("error field: got %v want response.body", body.Errors[0]["field"])
	}
	if counters.EventsRejectedBodyTooLargeTotal() != 1 {
		t.Errorf("body_too_large counter: got %d want 1", counters.EventsRejectedBodyTooLargeTotal())
	}
}

func TestEvents_BodyTooLarge_BodyCapZeroDisabled(t *testing.T) {
	t.Parallel()

	// bodyCap == 0 disables per-event body rejection; large body is truncated as before.
	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, 0, defaultMaxEvents)

	resp := postEvents(t, ts, `{
		"type":"response",
		"service":"svc",
		"status":200,
		"duration_ms":1,
		"body":"this is a very long body that would exceed a nonzero cap"
	}`, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("bodyCap=0 should accept any body size: got %d want 202", resp.StatusCode)
	}
	if counters.EventsRejectedBodyTooLargeTotal() != 0 {
		t.Errorf("body_too_large counter should be 0 when bodyCap=0: got %d", counters.EventsRejectedBodyTooLargeTotal())
	}
	drainQueue(t, q, 1)
}

// ---- Queue-full semantics (S-EVT-06) ----

// fillQueue enqueues n minimal records to saturate the queue.
func fillQueue(t *testing.T, q *capture.Queue, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		ok := q.Enqueue(&capture.ResponseEvent{ID: fmt.Sprintf("fill-%d", i)})
		if !ok {
			t.Fatalf("fillQueue: enqueue %d failed", i)
		}
	}
}

func TestEvents_SingleEvent_QueueFull_Returns503(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(1)
	fillQueue(t, q, 1)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	resp := postEvents(t, ts, `{"type":"response","service":"svc","status":200,"duration_ms":1}`, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("queue full single event: got %d want 503", resp.StatusCode)
	}
	var body struct {
		Errors []map[string]any `json:"errors"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Errors) == 0 || body.Errors[0]["field"] != "queue" {
		t.Errorf("expected queue field error, got %v", body.Errors)
	}
	if counters.EventsDroppedQueueFullTotal() != 1 {
		t.Errorf("dropped_queue_full counter: got %d want 1", counters.EventsDroppedQueueFullTotal())
	}
	if counters.EventsIngestedResponseTotal() != 0 {
		t.Errorf("ingested should be 0 on drop: got %d", counters.EventsIngestedResponseTotal())
	}
}

func TestEvents_Batch_AllDropped_Returns503(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(1)
	fillQueue(t, q, 1)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	body := buildBatch(validResponseItem("svc"), 3)
	resp := postEvents(t, ts, body, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("all dropped batch: got %d want 503", resp.StatusCode)
	}
	if counters.EventsDroppedQueueFullTotal() != 3 {
		t.Errorf("dropped_queue_full counter: got %d want 3", counters.EventsDroppedQueueFullTotal())
	}
	if counters.EventsIngestedResponseTotal() != 0 {
		t.Errorf("ingested should be 0 when all dropped: got %d", counters.EventsIngestedResponseTotal())
	}
}

func TestEvents_Batch_PartialDrop_Returns207(t *testing.T) {
	t.Parallel()

	// Queue cap=2, pre-fill 1 slot; 3 events → 1 accepted, 2 dropped.
	q := capture.NewQueue(2)
	fillQueue(t, q, 1)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	body := buildBatch(validResponseItem("svc"), 3)
	resp := postEvents(t, ts, body, testAdminToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMultiStatus {
		t.Errorf("partial drop batch: got %d want 207", resp.StatusCode)
	}
	var result struct {
		Accepted int `json:"accepted"`
		Dropped  int `json:"dropped"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Accepted != 1 {
		t.Errorf("accepted: got %d want 1", result.Accepted)
	}
	if result.Dropped != 2 {
		t.Errorf("dropped: got %d want 2", result.Dropped)
	}
	if counters.EventsDroppedQueueFullTotal() != 2 {
		t.Errorf("dropped_queue_full counter: got %d want 2", counters.EventsDroppedQueueFullTotal())
	}
	if counters.EventsIngestedResponseTotal() != 1 {
		t.Errorf("ingested: got %d want 1", counters.EventsIngestedResponseTotal())
	}
}

func TestEvents_Batch_NoneDropped_Returns202(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(10)
	ts, counters := newEventsServer(t, q, defaultBodyCap, defaultMaxEvents)

	body := buildBatch(validResponseItem("svc"), 2)
	resp := postEvents(t, ts, body, testAdminToken)
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("no drop batch: got %d want 202", resp.StatusCode)
	}
	if counters.EventsDroppedQueueFullTotal() != 0 {
		t.Errorf("dropped_queue_full should be 0: got %d", counters.EventsDroppedQueueFullTotal())
	}
	if counters.EventsIngestedResponseTotal() != 2 {
		t.Errorf("ingested: got %d want 2", counters.EventsIngestedResponseTotal())
	}
	drainQueue(t, q, 2)
}

// TestMetrics_EventsDroppedQueueFullAndBodyTooLarge verifies the new counters
// appear in /metrics output.
func TestMetrics_EventsDroppedQueueFullAndBodyTooLarge(t *testing.T) {
	t.Parallel()

	q := capture.NewQueue(1)
	counters := admin.NewEventsCounters()
	cfg := config.AdminConfig{
		Bind:       "127.0.0.1:0",
		Token:      testAdminToken,
		SessionTTL: time.Hour,
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{
		EventsRejectedBodyTooLargeTotal: counters.EventsRejectedBodyTooLargeTotal,
		EventsDroppedQueueFullTotal:     counters.EventsDroppedQueueFullTotal,
	}, admin.ServerOptions{
		Events: admin.EventsSources{
			Queue:            q,
			BodyCap:          defaultBodyCap,
			MaxEventsPayload: defaultMaxEvents,
			Counters:         counters,
		},
	})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)

	mResp, _ := http.Get(ts.URL + "/metrics")
	body, _ := io.ReadAll(mResp.Body)
	mResp.Body.Close()

	bodyStr := string(body)
	if !strings.Contains(bodyStr, `httpcatch_events_rejected_total{reason="body_too_large"}`) {
		t.Errorf("/metrics missing body_too_large rejection counter\n%s", bodyStr)
	}
	if !strings.Contains(bodyStr, "httpcatch_events_dropped_queue_full_total") {
		t.Errorf("/metrics missing httpcatch_events_dropped_queue_full_total\n%s", bodyStr)
	}
}
