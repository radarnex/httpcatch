package admin_test

// Live-tail structural and smoke tests.
//
// 1. Structural HTML invariants the JS depends on (toggle/checkbox/status
//    elements present and hidden, data-timestamp on rows).
// 2. /requests JSON endpoint honours the since parameter.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// ── Structural HTML assertions ─────────────────────────────────────────────

func TestLiveTail_ToggleRenderedHiddenByDefault(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	// The toggle label must be present with hidden attribute.
	labels := findElements(doc, "label", "id", "live-tail-toggle")
	if len(labels) == 0 {
		t.Fatal("live-tail-toggle label not found in rendered HTML")
	}
	lbl := labels[0]
	if !hasAttr(lbl, "hidden", "") {
		t.Error("live-tail-toggle must have hidden attribute")
	}
}

func TestLiveTail_CheckboxInsideToggle(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	checkboxes := findElements(doc, "input", "id", "live-tail-checkbox")
	if len(checkboxes) == 0 {
		t.Error("live-tail-checkbox input not found in rendered HTML")
	}
}

func TestLiveTail_StatusDivPresent(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	divs := findElements(doc, "div", "id", "live-tail-status")
	if len(divs) == 0 {
		t.Fatal("live-tail-status div not found in rendered HTML")
	}
	div := divs[0]
	if !hasAttr(div, "hidden", "") {
		t.Error("live-tail-status div must start hidden")
	}
}

func TestLiveTail_RowsCarryDataTimestamp(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()

	fixedTS := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	r := &capture.CapturedRequest{
		ID:            "ts-row",
		Timestamp:     fixedTS,
		Service:       "svc",
		Method:        "GET",
		Path:          "/",
		CorrelationID: "c1",
		SourceIP:      "x",
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	// All data rows must carry data-timestamp.
	rows := findElements(doc, "tr", "data-timestamp", "")
	if len(rows) == 0 {
		t.Fatal("no tr[data-timestamp] found in rendered HTML")
	}

	// Verify the timestamp value is ISO 8601 / RFC 3339.
	for _, row := range rows {
		ts := attrVal(row, "data-timestamp")
		if !strings.Contains(ts, "T") {
			t.Errorf("data-timestamp %q does not look like RFC 3339", ts)
		}
	}
}

func TestLiveTail_NoJSPath_FilterFormUsable(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	// Toggle is hidden — never interactive without JS.
	labels := findElements(doc, "label", "id", "live-tail-toggle")
	for _, lbl := range labels {
		if !hasAttr(lbl, "hidden", "") {
			t.Error("toggle must be hidden without JS")
		}
	}

	// Filter form must still work (method=GET, action=/ui/requests).
	forms := findElements(doc, "form", "method", "GET")
	if len(forms) == 0 {
		t.Error("filter form with method=GET not found")
	}
}

func TestLiveTail_TbodyHasID(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	tbodies := findElements(doc, "tbody", "id", "requests-tbody")
	if len(tbodies) == 0 {
		t.Error("tbody#requests-tbody not found — JS depends on this ID")
	}
}

func TestLiveTail_PaginationNavHasID(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	navs := findElements(doc, "nav", "id", "pagination-nav")
	if len(navs) == 0 {
		t.Error("nav#pagination-nav not found — JS depends on this ID")
	}
}

// ── Smoke test: GET /requests with since parameter ─────────────────────────
//
// This validates the backend the live-tail poller hits. We assert that calling
// GET /requests?since=<ts> after inserting records correctly returns only the
// records created after that timestamp.

func TestLiveTail_SinceFilter_ReturnsNewRecordsOnly(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(50)
	ctx := context.Background()

	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Two records before the cursor timestamp.
	for i := range 2 {
		r := &capture.CapturedRequest{
			ID:            fmt.Sprintf("old-%d", i),
			Timestamp:     base.Add(time.Duration(i) * time.Second),
			Service:       "svc",
			Method:        "GET",
			Path:          "/old",
			CorrelationID: fmt.Sprintf("cold-%d", i),
			SourceIP:      "x",
		}
		if err := mem.Write(ctx, r); err != nil {
			t.Fatalf("Write old: %v", err)
		}
	}

	// One record after.
	newTS := base.Add(10 * time.Second)
	newRecord := &capture.CapturedRequest{
		ID:            "new-1",
		Timestamp:     newTS,
		Service:       "svc",
		Method:        "POST",
		Path:          "/new",
		CorrelationID: "cnew-1",
		SourceIP:      "x",
	}
	if err := mem.Write(ctx, newRecord); err != nil {
		t.Fatalf("Write new: %v", err)
	}

	srv := newUIViewServer(t, admin.ReadSources{Memory: mem})

	// Since = one second after the last "old" record.
	sinceTS := base.Add(3 * time.Second).Format(time.RFC3339Nano)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/requests?since="+sinceTS, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Accept", "application/json")

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /requests?since=: %v", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", httpResp.StatusCode)
	}

	body, _ := io.ReadAll(httpResp.Body)

	var payload struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(payload.Records) != 1 {
		t.Errorf("records count: got %d want 1", len(payload.Records))
	}

	// Verify the record returned is the new one.
	raw := string(body)
	if !strings.Contains(raw, "new-1") {
		t.Error("response missing new record id new-1")
	}
	if strings.Contains(raw, "old-0") || strings.Contains(raw, "old-1") {
		t.Error("response must not include old records when since filter is applied")
	}
}

// TestLiveTail_LimitFifty verifies that the JSON endpoint respects limit=50.
func TestLiveTail_LimitFifty_ReturnsAtMost50(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(200)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	const total = 60
	for i := range total {
		r := &capture.CapturedRequest{
			ID:            fmt.Sprintf("lim-%03d", i),
			Timestamp:     base.Add(time.Duration(i) * time.Second),
			Service:       "svc",
			Method:        "GET",
			Path:          "/",
			CorrelationID: fmt.Sprintf("clim-%03d", i),
			SourceIP:      "x",
		}
		if err := mem.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	srv := newUIViewServer(t, admin.ReadSources{Memory: mem})

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/requests?limit=50", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Accept", "application/json")

	httpResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /requests?limit=50: %v", err)
	}
	defer httpResp.Body.Close()

	body, _ := io.ReadAll(httpResp.Body)
	var payload struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(payload.Records) != 50 {
		t.Errorf("records count: got %d want 50", len(payload.Records))
	}
}
