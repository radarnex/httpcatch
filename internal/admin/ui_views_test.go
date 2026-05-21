package admin_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/sinks"
)

// newUIViewServer builds a test server with the given readers wired in.
func newUIViewServer(t *testing.T, readers admin.ReadSources) *httptest.Server {
	t.Helper()
	cfg := config.AdminConfig{
		Bind:          "127.0.0.1:0",
		Token:         testAdminToken,
		SessionTTL:    time.Hour,
		SessionSecure: false,
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{}, admin.ServerOptions{Readers: readers})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts
}

// getUIPage sends an authenticated HTML GET request and returns the response.
func getUIPage(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// noFollowUIClient returns an http.Client that does not follow redirects.
func noFollowUIClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// parseHTML parses the response body as HTML and returns the root node.
func parseHTML(t *testing.T, r io.Reader) *html.Node {
	t.Helper()
	doc, err := html.Parse(r)
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}
	return doc
}

// findAttr walks the HTML tree and returns all elements of elType that have
// attrKey equal to attrVal, collecting the requested collectAttr value.
func findAttr(n *html.Node, elType, attrKey, attrVal, collectAttr string) []string {
	var results []string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == elType {
			matched := false
			collect := ""
			for _, a := range node.Attr {
				if a.Key == attrKey && a.Val == attrVal {
					matched = true
				}
				if a.Key == collectAttr {
					collect = a.Val
				}
			}
			if matched {
				results = append(results, collect)
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return results
}

// findElements returns all elements matching a tag name and an optional
// attribute filter (empty attrKey means no filter).
func findElements(n *html.Node, tag, attrKey, attrVal string) []*html.Node {
	var results []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == tag {
			if attrKey == "" {
				results = append(results, node)
			} else {
				for _, a := range node.Attr {
					if a.Key == attrKey && (attrVal == "" || a.Val == attrVal) {
						results = append(results, node)
						break
					}
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return results
}

// hasAttr returns true when node has the given attribute with the given value.
func hasAttr(n *html.Node, key, val string) bool {
	for _, a := range n.Attr {
		if a.Key == key && a.Val == val {
			return true
		}
	}
	return false
}

// attrVal returns the value of the named attribute or empty string.
func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// innerText collects all text content under a node.
func innerText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

// ── GET /ui/requests ──────────────────────────────────────────────────────────

func TestUIRequests_NoAuth_Redirects303(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	client := noFollowUIClient()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ui/requests", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/requests: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/login") {
		t.Errorf("Location %q: expected /login", loc)
	}
}

func TestUIRequests_WithBearer_Returns200HTML(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type: got %q want text/html; charset=utf-8", ct)
	}
}

func TestUIRequests_FilterFormPresent(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	// Confirm the filter form points to the right action.
	forms := findElements(doc, "form", "action", "/ui/requests")
	if len(forms) == 0 {
		t.Error("filter form with action=/ui/requests not found")
	}

	// Confirm the q, since, until hidden inputs exist.
	for _, name := range []string{"q", "since", "until"} {
		foundInput := findElements(doc, "input", "name", name)
		if len(foundInput) == 0 {
			t.Errorf("filter input with name=%q not found", name)
		}
	}
}

func TestUIRequests_FilterRoundTrip(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})

	q := "method:POST status:200 path:/api"
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ui/requests?q="+url.QueryEscape(q), nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// The q hidden input must round-trip the verbatim query string.
	if !strings.Contains(s, `value="`+q+`"`) {
		t.Errorf("q value not round-tripped into hidden input; got HTML %q", truncate(s, 400))
	}

	// One chip is rendered per parsed term.
	for _, want := range []string{`data-key="method"`, `data-key="status"`, `data-key="path"`} {
		if !strings.Contains(s, want) {
			t.Errorf("expected chip attribute %q in rendered HTML", want)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestUIRequests_ChipsForWildcardQuotedNegated(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})

	q := `-host:billing-api* path:"/signup/*" body:*login*`
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ui/requests?q="+url.QueryEscape(q), nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// Negated chip carries the is-negated class and the leading-minus glyph.
	if !strings.Contains(s, `class="qchip is-negated"`) {
		t.Errorf("negated chip class not rendered; HTML: %q", truncate(s, 800))
	}

	// data-token rounds-trips the literal token text (including `-`, quotes,
	// and `*`s) so chip removal can drop the exact substring from `q`.
	for _, want := range []string{
		`data-token="-host:billing-api*"`,
		`data-token="path:&#34;/signup/*&#34;"`,
		`data-token="body:*login*"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("expected token %q in HTML", want)
		}
	}

	// The visible chip value re-displays wildcards and quotes verbatim.
	for _, want := range []string{
		`data-value="billing-api*"`,
		`data-value="&#34;/signup/*&#34;"`,
		`data-value="*login*"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("expected visible value %q in HTML", want)
		}
	}
}

func TestUIRequests_HeaderChips_Rendering(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})

	// Mixed-case header name canonicalises to "User-Agent"; chip key reads
	// "header.User-Agent" so the operator sees the field they targeted.
	q := `headers:foo header.user-agent:client/0.3 -header.X-Trace-Id:abc`
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ui/requests?q="+url.QueryEscape(q), nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// Header chips carry the is-header class so CSS can distinguish them.
	if !strings.Contains(s, `class="qchip is-header"`) {
		t.Error(`expected at least one "qchip is-header" rendering`)
	}

	// The named-header chip's key includes the canonicalised header name.
	if !strings.Contains(s, `data-key="header.User-Agent"`) {
		t.Errorf("expected canonical header key in chip; HTML: %q", truncate(s, 800))
	}

	// data-token re-emits the operator-typed-ish form (canonical name preserved
	// so chip removal drops the equivalent token from `q`).
	for _, want := range []string{
		`data-token="headers:foo"`,
		`data-token="header.User-Agent:client/0.3"`,
		`data-token="-header.X-Trace-Id:abc"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("expected data-token %q in HTML", want)
		}
	}

	// The `H` pill marker appears for each header chip.
	if !strings.Contains(s, `class="qhdr"`) {
		t.Error(`expected "qhdr" pill marker on header chip`)
	}
}

func TestUIRequests_HeaderPlaceholder(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	want := "service:billing-api host:*.svc.local body:error -path:/health header.user-agent:client"
	if !strings.Contains(s, want) {
		t.Errorf("placeholder copy must match the locked example string %q", want)
	}
}

func TestUIRequests_FreeformChips_Rendering(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})

	q := `billing-api -orders service:foo`
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ui/requests?q="+url.QueryEscape(q), nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// Freeform chip carries is-freeform class and the "any" pill.
	if !strings.Contains(s, `class="qchip is-freeform"`) {
		t.Errorf("freeform chip class not rendered; HTML: %q", truncate(s, 800))
	}
	if !strings.Contains(s, `class="qany"`) {
		t.Error(`expected "qany" pill marker on freeform chip`)
	}

	// Negated freeform chip carries both is-freeform and is-negated.
	if !strings.Contains(s, `class="qchip is-freeform is-negated"`) {
		t.Errorf("negated freeform chip class not rendered; HTML: %q", truncate(s, 800))
	}

	// data-token round-trips the bare token (no key: prefix on freeform).
	if !strings.Contains(s, `data-token="billing-api"`) {
		t.Error(`expected data-token="billing-api" for freeform chip`)
	}
	if !strings.Contains(s, `data-token="-orders"`) {
		t.Error(`expected data-token="-orders" for negated freeform chip`)
	}

	// Field-qualified chip stays unchanged.
	if !strings.Contains(s, `data-token="service:foo"`) {
		t.Error(`expected data-token="service:foo" for field-qualified chip`)
	}
}

func TestUIRequests_ScanBanner_RendersForQualifyingQuery(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests?q="+url.QueryEscape("host:*api*"))
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)
	banners := findElements(doc, "div", "id", "scan-banner")
	if len(banners) != 1 {
		t.Fatalf("scan-banner: got %d elements want 1", len(banners))
	}
	banner := banners[0]
	if hasAttr(banner, "hidden", "") {
		t.Error("scan-banner: hidden attribute must be absent for a qualifying query")
	}
	text := innerText(banner)
	if !strings.Contains(text, "Unindexed scan") {
		t.Errorf("scan-banner text %q does not contain locked headline", text)
	}
	if !strings.Contains(text, "host/path/service") {
		t.Errorf("scan-banner text %q does not mention indexed dimensions", text)
	}
	// The banner has no close button — operators cannot dismiss it.
	if buttons := findElements(banner, "button", "", ""); len(buttons) != 0 {
		t.Errorf("scan-banner: expected no buttons, got %d", len(buttons))
	}
}

func TestUIRequests_ScanBanner_OmittedForNonQualifyingQuery(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})

	cases := []string{
		"",
		"host:billing-api",
		"host:billing-api*",
		"body:*foo*",
		"headers:foo",
		"header.user-agent:foo",
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			t.Parallel()
			path := "/ui/requests"
			if q != "" {
				path += "?q=" + url.QueryEscape(q)
			}
			resp := getUIPage(t, ts, path)
			defer resp.Body.Close()
			doc := parseHTML(t, resp.Body)
			banners := findElements(doc, "div", "id", "scan-banner")
			if len(banners) != 1 {
				t.Fatalf("scan-banner: got %d elements want 1 (always in DOM, just hidden)", len(banners))
			}
			if !hasAttr(banners[0], "hidden", "") {
				t.Errorf("scan-banner: expected hidden for non-qualifying query %q", q)
			}
		})
	}
}

func TestUIRequests_ScanBanner_NegationStillQualifies(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests?q="+url.QueryEscape("-host:*foo*"))
	defer resp.Body.Close()
	doc := parseHTML(t, resp.Body)
	banners := findElements(doc, "div", "id", "scan-banner")
	if len(banners) != 1 || hasAttr(banners[0], "hidden", "") {
		t.Error("scan-banner: expected visible for negated leading-wildcard query")
	}
}

func TestUIRequests_OrHintElement_PresentHiddenByDefault(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()
	doc := parseHTML(t, resp.Body)
	hints := findElements(doc, "div", "id", "or-hint")
	if len(hints) != 1 {
		t.Fatalf("or-hint: got %d elements want 1", len(hints))
	}
	if !hasAttr(hints[0], "hidden", "") {
		t.Error("or-hint: must be hidden by default — JS toggles it from input events")
	}
	dismiss := findElements(hints[0], "button", "id", "or-hint-dismiss")
	if len(dismiss) != 1 {
		t.Error("or-hint: missing dismiss button")
	}
}

func TestStatic_SearchQLJS_Served(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/static/searchql.js")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/javascript") {
		t.Errorf("Content-Type: got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, "parseQuery") {
		t.Error("searchql.js: expected parseQuery export")
	}
	if !strings.Contains(s, "isUnindexedScan") {
		t.Error("searchql.js: expected isUnindexedScan export")
	}
	if !strings.Contains(s, "shouldShowOrHint") {
		t.Error("searchql.js: expected shouldShowOrHint export")
	}
}

func TestLayout_LoadsSearchQLJS(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, `src="/static/searchql.js"`) {
		t.Error("layout must load /static/searchql.js before /static/app.js")
	}
	idxSql := strings.Index(s, `src="/static/searchql.js"`)
	idxApp := strings.Index(s, `src="/static/app.js"`)
	if idxSql < 0 || idxApp < 0 || idxSql > idxApp {
		t.Errorf("searchql.js script tag must appear before app.js (sql=%d app=%d)", idxSql, idxApp)
	}
}

func TestUIRequests_InvalidFilter_InlineError(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests?q="+url.QueryEscape("method:INVALID_VERB"))
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// Page renders successfully (not a 400 JSON error).
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200 (inline error)", resp.StatusCode)
	}
	// Inline error element must be present.
	if !strings.Contains(s, "inline-error") {
		t.Error("inline-error class not found in rendered HTML")
	}
	// JSON 400 must not be sent.
	if strings.Contains(s, `"field"`) {
		t.Error("JSON error response must not be returned to HTML clients")
	}
}

func TestUIRequests_TableColumnsPresent(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	r := &capture.CapturedRequest{
		ID:            "row-test",
		Timestamp:     time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		Service:       "orders",
		Method:        "POST",
		Path:          "/api/orders",
		CorrelationID: "corr-row",
		SourceIP:      "1.2.3.4",
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	for _, want := range []string{"orders", "POST", "/api/orders", "request"} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered HTML missing %q", want)
		}
	}
}

func TestUIRequests_OrphanRowHasAmberBadge(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()

	// Write an orphan response event (no matching captured request).
	ev := &capture.ResponseEvent{
		ID:            "orphan-evt-1",
		Timestamp:     time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		CorrelationID: "orphan-corr-1",
		Service:       "payments",
		ServiceSource: "app",
		Status:        200,
		Headers:       map[string][]string{},
		Body:          []byte{},
	}
	if err := mem.Write(ctx, ev); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)
	body, _ := io.ReadAll(strings.NewReader(""))
	_ = body

	// Find badge elements with badge-orphan class.
	badges := findElements(doc, "span", "class", "badge badge-orphan")
	if len(badges) == 0 {
		t.Error("orphan badge with class badge-orphan not found")
	}
	// The orphan badge must have role="status".
	for _, b := range badges {
		if !hasAttr(b, "role", "status") {
			t.Error("orphan badge missing role=status")
		}
	}
	// Row containing the orphan must have row-orphan class.
	orphanRows := findElements(doc, "tr", "class", "row-orphan")
	if len(orphanRows) == 0 {
		t.Error("row-orphan class not found on orphan row")
	}
}

func TestUIRequests_PaginationLinks(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(100)
	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	const total = 5

	for i := range total {
		r := &capture.CapturedRequest{
			ID:            fmt.Sprintf("pag-%02d", i),
			Timestamp:     base.Add(time.Duration(i) * time.Second),
			Service:       "svc",
			Method:        "GET",
			Path:          "/",
			CorrelationID: fmt.Sprintf("c%d", i),
			SourceIP:      "x",
		}
		if err := mem.Write(ctx, r); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})

	// First page with limit=3: should have a Next cursor link.
	resp := getUIPage(t, ts, "/ui/requests?limit=3")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	// Find anchor elements with page-link class that have an href.
	links := findElements(doc, "a", "class", "page-link")
	var nextHref string
	for _, l := range links {
		if strings.Contains(innerText(l), "Next") {
			nextHref = attrVal(l, "href")
		}
	}
	if nextHref == "" {
		t.Error("Next pagination link not found or has no href")
	}
	if !strings.Contains(nextHref, "cursor=") {
		t.Errorf("Next link href %q does not contain cursor= param", nextHref)
	}
}

func TestUIRequests_PaginationDisabledWhenNoMore(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	r := &capture.CapturedRequest{
		ID:            "only-row",
		Timestamp:     time.Now(),
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

	// Next should be disabled (a span with aria-disabled, not an anchor).
	disabled := findElements(doc, "span", "aria-disabled", "true")
	var nextDisabled bool
	for _, el := range disabled {
		if strings.Contains(innerText(el), "Next") {
			nextDisabled = true
		}
	}
	if !nextDisabled {
		t.Error("Next link should be disabled (span with aria-disabled=true) when no more pages")
	}
}

func TestUIRequests_RowLinksToDetail(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	r := &capture.CapturedRequest{
		ID:            "link-test-id",
		Timestamp:     time.Now(),
		Service:       "svc",
		Method:        "GET",
		Path:          "/api",
		CorrelationID: "c1",
		SourceIP:      "x",
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "/ui/requests/link-test-id") {
		t.Error("rendered HTML missing link to /ui/requests/link-test-id")
	}
}

func TestUIRequests_WarningChipsPresent(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)
	ids := make(map[string]bool)
	findIDs(doc, ids)

	for _, id := range []string{"chip-unredacted", "chip-dropped", "chip-redaction-errors", "chip-service", "chip-correlation", "buildinfo"} {
		if !ids[id] {
			t.Errorf("layout element missing: id=%q", id)
		}
	}
}

func TestUIRequests_NoJS_FilterFormUsable(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	resp := getUIPage(t, ts, "/ui/requests")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	// Smoke test: the filter form has method=GET and action=/ui/requests.
	forms := findElements(doc, "form", "method", "GET")
	if len(forms) == 0 {
		t.Error("no form with method=GET found (required for no-JS operation)")
	}
	var hasCorrectAction bool
	for _, f := range forms {
		if attrVal(f, "action") == "/ui/requests" {
			hasCorrectAction = true
		}
	}
	if !hasCorrectAction {
		t.Error("filter form with action=/ui/requests not found")
	}

	// Pagination links (anchors) must be present without JS.
	links := findElements(doc, "a", "class", "page-link")
	// There may be no links when all rows fit on one page — that's fine.
	// The point is they render as <a> elements (not JS-only), or as disabled spans.
	// Disabled state must use aria-disabled, not hidden or display:none.
	disabled := findElements(doc, "span", "aria-disabled", "true")
	if len(links) == 0 && len(disabled) == 0 {
		t.Error("no pagination elements found; expected at least disabled spans")
	}
}

// ── GET /ui/requests/{id} ─────────────────────────────────────────────────────

func TestUIRequestDetail_WithBearer_Returns200HTML(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	r := &capture.CapturedRequest{
		ID:            "detail-ok",
		Timestamp:     time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		Service:       "svc",
		Method:        "GET",
		Path:          "/api",
		CorrelationID: "c1",
		SourceIP:      "1.2.3.4",
		Headers:       map[string][]string{"Host": {"example.com"}},
		Query:         map[string][]string{},
		Cookies:       []capture.Cookie{},
		Body:          []byte{},
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})
	resp := getUIPage(t, ts, "/ui/requests/detail-ok")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type: got %q", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	for _, want := range []string{"svc", "GET", "/api", "1.2.3.4", "example.com"} {
		if !strings.Contains(s, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
}

func TestUIRequestDetail_UnknownID_Returns404HTML(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})
	resp := getUIPage(t, ts, "/ui/requests/no-such-id")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: got %d want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type: got %q want text/html; charset=utf-8", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "no-such-id") {
		t.Error("404 page: expected record id in the response body")
	}
}

func TestUIRequestDetail_NoAuth_Redirects303(t *testing.T) {
	t.Parallel()

	ts := newUIViewServer(t, admin.ReadSources{})
	client := noFollowUIClient()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ui/requests/some-id", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/requests/some-id: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", resp.StatusCode)
	}
}

func TestUIRequestDetail_WithEvents_RendersTimeline(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/test.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	req := &capture.CapturedRequest{
		ID:                "detail-evt",
		Timestamp:         base,
		Service:           "orders",
		Method:            "POST",
		Path:              "/api/orders",
		CorrelationID:     "corr-detail-evt",
		SourceIP:          "1.2.3.4",
		Headers:           map[string][]string{"Content-Type": {"application/json"}},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte(`{"item":"widget"}`),
		ContentType:       "application/json",
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	if err := sqliteSink.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}

	ev := &capture.ResponseEvent{
		ID:            "detail-resp-evt",
		Timestamp:     base.Add(100 * time.Millisecond),
		CorrelationID: "corr-detail-evt",
		Service:       "orders",
		ServiceSource: "app",
		Status:        200,
		Headers:       map[string][]string{"Content-Type": {"application/json"}},
		Body:          []byte(`{"ok":true}`),
		ContentType:   "application/json",
		DurationMS:    42,
	}
	if err := sqliteSink.Write(ctx, ev); err != nil {
		t.Fatalf("Write ev: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{SQLite: sqliteSink})
	resp := getUIPage(t, ts, "/ui/requests/detail-evt")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// Request fields.
	if !strings.Contains(s, "POST") {
		t.Error("detail: missing method POST")
	}
	// Response event rendered.
	if !strings.Contains(s, "Response") {
		t.Error("detail: missing Response timeline event")
	}
	if !strings.Contains(s, "200") {
		t.Error("detail: missing status 200")
	}
}

func TestUIRequestDetail_OutboundEvent_TwoHalves(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/outbound.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	req := &capture.CapturedRequest{
		ID:                "req-outbound",
		Timestamp:         base,
		Service:           "gateway",
		Method:            "GET",
		Path:              "/api/data",
		CorrelationID:     "corr-outbound",
		SourceIP:          "1.2.3.4",
		Headers:           map[string][]string{},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte{},
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	if err := sqliteSink.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}

	ev := &capture.OutboundEvent{
		ID:            "outbound-evt-1",
		Timestamp:     base.Add(50 * time.Millisecond),
		CorrelationID: "corr-outbound",
		Service:       "gateway",
		ServiceSource: "app",
		DurationMS:    38,
		Request: capture.OutboundRequestHalf{
			Method:  "POST",
			Path:    "/payments",
			Headers: map[string][]string{"Content-Type": {"application/json"}},
			Body:    []byte(`{"amount":100}`),
		},
		Response: &capture.OutboundResponseHalf{
			Status:  201,
			Headers: map[string][]string{},
			Body:    []byte(`{"paid":true}`),
		},
	}
	if err := sqliteSink.Write(ctx, ev); err != nil {
		t.Fatalf("Write ev: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{SQLite: sqliteSink})
	resp := getUIPage(t, ts, "/ui/requests/req-outbound")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	if !strings.Contains(s, "Outbound") {
		t.Error("detail: missing Outbound event label")
	}
	if !strings.Contains(s, "/payments") {
		t.Error("detail: missing outbound request path /payments")
	}
	if !strings.Contains(s, "201") {
		t.Error("detail: missing outbound response status 201")
	}
}

func TestUIRequestDetail_OutboundEvent_NullResponse(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/null-resp.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	req := &capture.CapturedRequest{
		ID:                "req-null-resp",
		Timestamp:         base,
		Service:           "svc",
		Method:            "GET",
		Path:              "/",
		CorrelationID:     "corr-null",
		SourceIP:          "x",
		Headers:           map[string][]string{},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte{},
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	if err := sqliteSink.Write(ctx, req); err != nil {
		t.Fatalf("Write req: %v", err)
	}

	ev := &capture.OutboundEvent{
		ID:            "outbound-null",
		Timestamp:     base.Add(10 * time.Millisecond),
		CorrelationID: "corr-null",
		Service:       "svc",
		ServiceSource: "app",
		DurationMS:    5,
		Request: capture.OutboundRequestHalf{
			Method:  "GET",
			Path:    "/external",
			Headers: map[string][]string{},
			Body:    []byte{},
		},
		Response: nil, // call never completed
	}
	if err := sqliteSink.Write(ctx, ev); err != nil {
		t.Fatalf("Write ev: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{SQLite: sqliteSink})
	resp := getUIPage(t, ts, "/ui/requests/req-null-resp")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	if !strings.Contains(s, "no response") {
		t.Error("detail: missing 'no response' indicator for null outbound response")
	}
}

func TestUIRequestDetail_BodyTruncationIndicator(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	r := &capture.CapturedRequest{
		ID:               "trunc-req",
		Timestamp:        time.Now(),
		Service:          "svc",
		Method:           "POST",
		Path:             "/big",
		CorrelationID:    "c-trunc",
		SourceIP:         "x",
		Headers:          map[string][]string{},
		Query:            map[string][]string{},
		Cookies:          []capture.Cookie{},
		Body:             []byte("partial body content"),
		ContentType:      "text/plain",
		BodyTruncated:    true,
		BodyOriginalSize: 1048576,
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})
	resp := getUIPage(t, ts, "/ui/requests/trunc-req")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	if !strings.Contains(s, "truncated") {
		t.Error("detail: missing truncation indicator")
	}
	if !strings.Contains(s, "1048576") {
		t.Error("detail: missing original size in truncation indicator")
	}
}

func TestUIRequestDetail_CopyAsCURLButton_HiddenAttribute(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	r := &capture.CapturedRequest{
		ID:            "curl-req",
		Timestamp:     time.Now(),
		Service:       "svc",
		Method:        "GET",
		Path:          "/api",
		CorrelationID: "c-curl",
		SourceIP:      "x",
		Headers:       map[string][]string{},
		Query:         map[string][]string{},
		Cookies:       []capture.Cookie{},
		Body:          []byte{},
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})
	resp := getUIPage(t, ts, "/ui/requests/curl-req")
	defer resp.Body.Close()

	doc := parseHTML(t, resp.Body)

	// The Copy-as-cURL button must be in the DOM with id=curl-copy-btn.
	buttons := findElements(doc, "button", "id", "curl-copy-btn")
	if len(buttons) == 0 {
		t.Fatal("curl-copy-btn button not found in detail page")
	}
	btn := buttons[0]

	// Button must have the hidden attribute set so non-JS users never see it.
	if !hasAttr(btn, "hidden", "") {
		t.Error("curl-copy-btn must have hidden attribute in HTML (JS removes it)")
	}
}

func TestUIRequestDetail_JSONBodyPrettyPrinted(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()
	r := &capture.CapturedRequest{
		ID:            "json-body",
		Timestamp:     time.Now(),
		Service:       "svc",
		Method:        "POST",
		Path:          "/api",
		CorrelationID: "c-json",
		SourceIP:      "x",
		Headers:       map[string][]string{"Content-Type": {"application/json"}},
		Query:         map[string][]string{},
		Cookies:       []capture.Cookie{},
		Body:          []byte(`{"key":"value"}`),
		ContentType:   "application/json",
	}
	if err := mem.Write(ctx, r); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})
	resp := getUIPage(t, ts, "/ui/requests/json-body")
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	// Pretty-printed JSON renders with indented lines; the HTML-escaped quote is &#34;.
	// We verify the body-json class is applied (confirms JSON detection) and
	// that the key appears somewhere in the rendered output.
	if !strings.Contains(s, "body-json") {
		t.Error("detail: body-json class not found for JSON body")
	}
	// The key "key" must appear in the HTML (possibly HTML-escaped).
	if !strings.Contains(s, "key") {
		t.Error("detail: JSON body key not found in rendered HTML")
	}
	// Indentation: pretty-printed JSON produces lines starting with spaces.
	// html/template preserves whitespace in pre elements.
	if !strings.Contains(s, "\n  ") {
		t.Error("detail: JSON body does not appear to be indented (no newline+spaces)")
	}
}

func TestUIDetail_OrphanResponseEventID_Returns200(t *testing.T) {
	t.Parallel()

	mem := sinks.NewMemorySink(10)
	ctx := context.Background()

	ev := &capture.ResponseEvent{
		ID:            "orphan-resp-root",
		Timestamp:     time.Date(2026, 5, 18, 14, 0, 0, 0, time.UTC),
		CorrelationID: "corr-orphan-resp",
		Service:       "billing",
		ServiceSource: "header",
		Status:        202,
		Headers:       map[string][]string{"Content-Type": {"application/json"}},
		Body:          []byte(`{"queued":true}`),
		ContentType:   "application/json",
		DurationMS:    15,
	}
	if err := mem.Write(ctx, ev); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem})
	resp := getUIPage(t, ts, "/ui/requests/orphan-resp-root")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type: got %q want text/html; charset=utf-8", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	if !strings.Contains(s, "billing") {
		t.Error("event detail: service 'billing' not found in rendered HTML")
	}
	if !strings.Contains(s, "corr-orphan-resp") {
		t.Error("event detail: correlation_id 'corr-orphan-resp' not found in rendered HTML")
	}
	if !strings.Contains(s, "202") {
		t.Error("event detail: status 202 not found in rendered HTML")
	}
	if !strings.Contains(s, "No other correlated records") {
		t.Error("event detail: 'No other correlated records' not found in rendered HTML")
	}
	// No cURL button on event-rooted pages.
	if strings.Contains(s, "curl-copy-btn") {
		t.Error("event detail: curl-copy-btn must not appear on event-rooted detail page")
	}
}

func TestUIDetail_OrphanOutboundEventID_Returns200(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/orphan-outbound.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	ctx := context.Background()

	ev := &capture.OutboundEvent{
		ID:            "orphan-out-root",
		Timestamp:     time.Date(2026, 5, 18, 14, 30, 0, 0, time.UTC),
		CorrelationID: "corr-orphan-out",
		Service:       "gateway",
		ServiceSource: "header",
		DurationMS:    22,
		Request: capture.OutboundRequestHalf{
			Method:  "DELETE",
			Path:    "/external/resource",
			Headers: map[string][]string{},
			Body:    []byte{},
		},
		Response: &capture.OutboundResponseHalf{
			Status:  204,
			Headers: map[string][]string{},
			Body:    []byte{},
		},
	}
	if err := sqliteSink.Write(ctx, ev); err != nil {
		t.Fatalf("Write: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{SQLite: sqliteSink})
	resp := getUIPage(t, ts, "/ui/requests/orphan-out-root")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	s := string(body)

	if !strings.Contains(s, "gateway") {
		t.Error("outbound event detail: service 'gateway' not found")
	}
	if !strings.Contains(s, "corr-orphan-out") {
		t.Error("outbound event detail: correlation_id 'corr-orphan-out' not found")
	}
	if !strings.Contains(s, "/external/resource") {
		t.Error("outbound event detail: outbound path not found")
	}
	if !strings.Contains(s, "204") {
		t.Error("outbound event detail: response status 204 not found")
	}
}

// ── GET / redirect ────────────────────────────────────────────────────────────

func TestRoot_WithSession_RedirectsToUIRequests(t *testing.T) {
	t.Parallel()

	base, client := newUIServer(t, testAdminToken)
	cookie := sessionCookieFor(t, base, testAdminToken, client)

	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("Accept", "text/html")
	req.AddCookie(cookie)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/ui/requests" {
		t.Errorf("Location: got %q want /ui/requests", loc)
	}
}

func TestRoot_WithoutSession_RedirectsToLogin(t *testing.T) {
	t.Parallel()

	base, client := newUIServer(t, testAdminToken)

	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET / without session: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/login") {
		t.Errorf("Location %q: expected /login", loc)
	}
}

// ── Integration test ──────────────────────────────────────────────────────────

func TestUIViews_Integration(t *testing.T) {
	t.Parallel()

	// Boot with both memory and sqlite.
	mem := sinks.NewMemorySink(100)
	dir := t.TempDir()
	sqliteSink, err := sinks.NewSQLiteSink(dir + "/int.db")
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = sqliteSink.Close() })

	ctx := context.Background()
	base := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	// Write a captured request to both sinks.
	req := &capture.CapturedRequest{
		ID:                "int-req",
		Timestamp:         base,
		Service:           "web",
		Method:            "GET",
		Path:              "/home",
		CorrelationID:     "corr-int",
		SourceIP:          "10.0.0.1",
		Headers:           map[string][]string{"Host": {"example.com"}},
		Query:             map[string][]string{},
		Cookies:           []capture.Cookie{},
		Body:              []byte{},
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
	if err := mem.Write(ctx, req); err != nil {
		t.Fatalf("mem.Write req: %v", err)
	}
	if err := sqliteSink.Write(ctx, req); err != nil {
		t.Fatalf("sqlite.Write req: %v", err)
	}

	// Write a correlated response event.
	ev := &capture.ResponseEvent{
		ID:            "int-resp-evt",
		Timestamp:     base.Add(200 * time.Millisecond),
		CorrelationID: "corr-int",
		Service:       "web",
		ServiceSource: "app",
		Status:        200,
		Headers:       map[string][]string{"Content-Type": {"application/json"}},
		Body:          []byte(`{"ok":true}`),
		ContentType:   "application/json",
		DurationMS:    30,
	}
	if err := mem.Write(ctx, ev); err != nil {
		t.Fatalf("mem.Write ev: %v", err)
	}
	if err := sqliteSink.Write(ctx, ev); err != nil {
		t.Fatalf("sqlite.Write ev: %v", err)
	}

	// Write an orphan response event (no matching request).
	orphan := &capture.ResponseEvent{
		ID:            "int-orphan",
		Timestamp:     base.Add(time.Second),
		CorrelationID: "corr-orphan-xyz",
		Service:       "payments",
		ServiceSource: "app",
		Status:        500,
		Headers:       map[string][]string{},
		Body:          []byte{},
		DurationMS:    1,
	}
	if err := mem.Write(ctx, orphan); err != nil {
		t.Fatalf("mem.Write orphan: %v", err)
	}
	if err := sqliteSink.Write(ctx, orphan); err != nil {
		t.Fatalf("sqlite.Write orphan: %v", err)
	}

	ts := newUIViewServer(t, admin.ReadSources{Memory: mem, SQLite: sqliteSink})

	// Step 1: fetch the list page.
	listResp := getUIPage(t, ts, "/ui/requests")
	if listResp.StatusCode != http.StatusOK {
		listResp.Body.Close()
		t.Fatalf("GET /ui/requests: status %d", listResp.StatusCode)
	}
	listBody, _ := io.ReadAll(listResp.Body)
	listResp.Body.Close()
	ls := string(listBody)

	if !strings.Contains(ls, "int-req") {
		t.Error("list: request id int-req not rendered")
	}
	if !strings.Contains(ls, "int-orphan") {
		t.Error("list: orphan id int-orphan not rendered")
	}
	if !strings.Contains(ls, "badge-orphan") {
		t.Error("list: orphan badge not rendered")
	}

	// Step 2: fetch the detail page for the captured request.
	detailResp := getUIPage(t, ts, "/ui/requests/int-req")
	if detailResp.StatusCode != http.StatusOK {
		detailResp.Body.Close()
		t.Fatalf("GET /ui/requests/int-req: status %d", detailResp.StatusCode)
	}
	detailBody, _ := io.ReadAll(detailResp.Body)
	detailResp.Body.Close()
	ds := string(detailBody)

	if !strings.Contains(ds, "web") {
		t.Error("detail: service web not rendered")
	}
	if !strings.Contains(ds, "Response") {
		t.Error("detail: Response timeline event not rendered")
	}
	if !strings.Contains(ds, "200") {
		t.Error("detail: response status 200 not rendered")
	}

	// Step 3: the orphan event detail renders a 200 HTML page with event fields.
	orphanDetailResp := getUIPage(t, ts, "/ui/requests/int-orphan")
	if orphanDetailResp.StatusCode != http.StatusOK {
		orphanDetailResp.Body.Close()
		t.Fatalf("GET /ui/requests/int-orphan (orphan): status %d want 200", orphanDetailResp.StatusCode)
	}
	orphanDetailBody, _ := io.ReadAll(orphanDetailResp.Body)
	orphanDetailResp.Body.Close()
	if ct := orphanDetailResp.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("orphan detail: Content-Type %q want text/html", ct)
	}
	ods := string(orphanDetailBody)
	// The page must render the event's service and correlation id.
	if !strings.Contains(ods, "payments") {
		t.Error("orphan detail: service 'payments' not found in rendered HTML")
	}
	if !strings.Contains(ods, "corr-orphan-xyz") {
		t.Error("orphan detail: correlation_id 'corr-orphan-xyz' not found in rendered HTML")
	}
	// Must be HTML, not JSON.
	if strings.HasPrefix(strings.TrimSpace(ods), "{") {
		t.Error("orphan detail: response looks like JSON, expected HTML")
	}

	// Step 4: test the filter by service via the new q parameter.
	filterResp := getUIPage(t, ts, "/ui/requests?q="+url.QueryEscape("service:web"))
	if filterResp.StatusCode != http.StatusOK {
		filterResp.Body.Close()
		t.Fatalf("GET /ui/requests?q=service:web: status %d", filterResp.StatusCode)
	}
	filterBody, _ := io.ReadAll(filterResp.Body)
	filterResp.Body.Close()
	fs := string(filterBody)

	// The filtered page renders one chip per parsed term.
	if !strings.Contains(fs, `data-value="web"`) {
		t.Error("filter: service:web chip not rendered")
	}

	// Step 5: the list page URL is the same shape as the JSON API.
	u, _ := url.Parse(ts.URL + "/ui/requests?q=" + url.QueryEscape("service:web method:GET"))
	if u.Query().Get("q") != "service:web method:GET" {
		t.Errorf("filter URL does not carry q parameter correctly: got %q", u.Query().Get("q"))
	}
}
