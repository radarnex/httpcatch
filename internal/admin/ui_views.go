package admin

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/inspect"
	"github.com/radarnex/httpcatch/internal/searchql"
)

// httpMethods is the ordered list of HTTP verbs shown in the method dropdown.
var httpMethods = []string{
	"GET", "HEAD", "POST", "PUT", "DELETE",
	"CONNECT", "OPTIONS", "TRACE", "PATCH",
}

// servicesSince is the lookback window for populating the service dropdown.
const servicesSince = 24 * time.Hour

// defaultExplorerWindow is the time range the UI applies when the operator
// opens /ui/requests with no since/until in the URL. The picker preset list
// starts at 15m and the trigger placeholder reads "Past 15 minutes", so the
// default matches what the operator sees in the picker.
const defaultExplorerWindow = 15 * time.Minute

// listTmpl, detailTmpl, and eventDetailTmpl are parsed once at startup from
// the embedded FS. Each is parsed together with layout.html so the
// {{define "body"}} block in each page template overrides the {{block "body"}}
// slot in the layout.
var (
	listTmpl          = template.Must(template.ParseFS(uiFS, "ui/layout.html", "ui/requests_list.html"))
	detailTmpl        = template.Must(template.ParseFS(uiFS, "ui/layout.html", "ui/requests_detail.html"))
	eventDetailTmpl   = template.Must(template.ParseFS(uiFS, "ui/layout.html", "ui/events_detail.html"))
	servicesTmpl      = template.Must(template.ParseFS(uiFS, "ui/layout.html", "ui/services.html"))
	configurationTmpl = template.Must(template.ParseFS(uiFS, "ui/layout.html", "ui/configuration.html"))
)

// rootRedirectHandler redirects the root URL to the request list.
// Unauthenticated browsers are caught by the auth middleware first, which sends
// them to /login with ?next=/ui/requests, and they arrive here only after auth.
func rootRedirectHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/requests", http.StatusSeeOther)
	}
}

// listQueryView carries the raw filter strings as entered by the operator,
// round-tripped from the URL into the form so the operator sees the current
// filter state. Q is the verbatim search query; Chips renders one entry per
// parsed term and is empty when q failed to parse.
type listQueryView struct {
	Q        string
	SinceRaw string
	UntilRaw string
	Chips    []chipView
}

// chipView is one server-rendered chip above the search input. Token is the
// full reconstructed token text (including any leading `-`, wildcards, and
// surrounding quotes) so the chip's `×` button can drop the exact substring
// from the submitted `q`. Key and Value drive the chip's visual rendering;
// Negated toggles the dim/minus styling. IsHeader marks `headers:` and
// `header.<name>:` chips so the template can give them a distinct visual;
// IsFreeform marks bare (no `key:`) terms so they render with an "any" pill
// instead of the `key:` segment.
type chipView struct {
	Token      string
	Key        string
	Value      string
	Negated    bool
	IsHeader   bool
	IsFreeform bool
}

// chipsFromQuery walks a parsed searchql.Query and produces the chip list for
// server-side rendering. Status terms render using their original form (e.g.
// "200" or "2xx"); quoted, wildcarded, and negated terms round-trip into both
// the visible Value and the data-token round-trip string. Per-header terms
// (Field == FieldHeader) carry the canonical name in the visible Key so the
// chip reads `header.User-Agent` rather than just `header`.
func chipsFromQuery(q searchql.Query) []chipView {
	if len(q.Terms) == 0 {
		return nil
	}
	chips := make([]chipView, 0, len(q.Terms))
	for _, t := range q.Terms {
		value := chipDisplayValue(t)
		key := string(t.Field)
		if t.Field == searchql.FieldHeader {
			key = "header." + t.HeaderName
		}
		freeform := t.Field == ""
		var token string
		if freeform {
			token = value
		} else {
			token = key + ":" + value
		}
		if t.Negated {
			token = "-" + token
		}
		chips = append(chips, chipView{
			Token:      token,
			Key:        key,
			Value:      value,
			Negated:    t.Negated,
			IsHeader:   t.Field == searchql.FieldHeaders || t.Field == searchql.FieldHeader,
			IsFreeform: freeform,
		})
	}
	return chips
}

// chipDisplayValue renders a term's value back to its surface form: status
// terms use the original Exact/Class text; quoted values are wrapped in `"…"`
// with `"` re-escaped; wildcarded values prepend or append `*` according to
// the parsed shape; bare values pass through.
func chipDisplayValue(t searchql.Term) string {
	if t.StatusFilter != nil {
		if t.StatusFilter.Exact != 0 {
			return fmt.Sprintf("%d", t.StatusFilter.Exact)
		}
		return t.StatusFilter.Class
	}
	if t.QuotedLiteral {
		return `"` + strings.ReplaceAll(t.Value, `"`, `\"`) + `"`
	}
	switch t.Wildcard {
	case searchql.WildcardPrefix:
		return t.Value + "*"
	case searchql.WildcardSubstring:
		return "*" + t.Value + "*"
	default:
		return t.Value
	}
}

// rowView wraps a RootRow for the list template.
type rowView struct {
	inspect.RootRow
}

// EventCountText returns a formatted event count, or an empty string for orphan rows.
func (r rowView) EventCountText() string {
	if r.RootRow.EventCount == nil {
		return ""
	}
	return fmt.Sprintf("%d", *r.RootRow.EventCount)
}

// EventCountNonZero reports whether the row has at least one correlated event.
// Used by the template to apply the "has" highlight class to the event pill.
func (r rowView) EventCountNonZero() bool {
	return r.RootRow.EventCount != nil && *r.RootRow.EventCount > 0
}

// StatusText returns the formatted status code or an empty string when unknown.
func (r rowView) StatusText() string {
	if r.RootRow.Status == nil {
		return ""
	}
	return fmt.Sprintf("%d", *r.RootRow.Status)
}

// StatusClassCSS returns the status class suffix used by the CSS `.s-{class}`
// selectors (2xx, 3xx, 4xx, 5xx, or other). Empty string when status unknown.
func (r rowView) StatusClassCSS() string {
	if r.RootRow.Status == nil {
		return ""
	}
	return httpStatusClass(*r.RootRow.Status)
}

// listPageData is the template data for GET /ui/requests.
type listPageData struct {
	Page     string
	Error    string
	Services []string
	Methods  []string
	Query    listQueryView
	Rows     []rowView
	// UnindexedScan is true when the parsed query has the structural
	// cost-class property — a leading wildcard against an indexed field. The
	// template renders the amber warning banner under the search bar when set.
	UnindexedScan bool
	// NextURL is the full href for the Next pagination link. It uses
	// template.URL so Go's html/template does not re-encode the query string.
	// An empty value means there is no further page.
	NextURL template.URL
}

// requestListHandler returns an http.HandlerFunc for GET /ui/requests.
func requestListHandler(rs ReadSources) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := withInspectTimeout(r.Context(), rs.QueryTimeout)
		defer cancel()

		data := listPageData{
			Page:    "explorer",
			Methods: httpMethods,
		}

		// Populate the service dropdown from whichever reader is available.
		rd := rs.Memory
		if rd == nil {
			rd = rs.SQLite
		}
		if rd != nil {
			since := time.Now().Add(-servicesSince)
			if svcs, err := rd.ServicesSeen(ctx, since); err == nil {
				data.Services = svcs
			}
		}

		// Round-trip raw filter strings into the form. When neither since nor
		// until is supplied, fall back to the picker's default window so the
		// trigger label and the returned rows describe the same time range.
		// The live-tail mode (`live=1` in the URL hash) and cursor pagination
		// both opt out of the default: live tail polls with its own derived
		// since, and pagination must preserve the original query.
		vals := r.URL.Query()
		if vals.Get("since") == "" && vals.Get("until") == "" && vals.Get("cursor") == "" {
			now := time.Now().UTC()
			// RFC3339Nano preserves the full sub-second precision so a record
			// captured a few milliseconds before `now` does not get filtered
			// out by a second-rounded `until`.
			vals.Set("since", now.Add(-defaultExplorerWindow).Format(time.RFC3339Nano))
			vals.Set("until", now.Format(time.RFC3339Nano))
		}
		data.Query = listQueryView{
			Q:        vals.Get("q"),
			SinceRaw: vals.Get("since"),
			UntilRaw: vals.Get("until"),
		}

		q, fieldErrs := parseInspectQuery(vals)
		if len(fieldErrs) > 0 {
			data.Error = fieldErrs[0].Error()
			renderList(w, data, http.StatusOK)
			return
		}
		data.Query.Chips = chipsFromQuery(q.Query)
		data.UnindexedScan = q.Query.IsUnindexedScan()
		if q.Query.IsUnindexedScan() && !requiresNarrowing(q) {
			data.Error = "leading-wildcard queries require a time range (since/until) or an exact service: term"
			renderList(w, data, http.StatusBadRequest)
			return
		}

		rows, nextCur, _, err := gatherRoots(ctx, q, rs.Memory, rs.SQLite)
		if err != nil {
			http.Error(w, fmt.Sprintf("read error: %v", err), http.StatusInternalServerError)
			return
		}

		for _, rr := range rows {
			data.Rows = append(data.Rows, rowView{rr})
		}
		if nextCur != nil {
			data.NextURL = template.URL("/ui/requests?" + overrideQueryParam(vals, "cursor", nextCur.Encode()))
		}

		renderList(w, data, http.StatusOK)
	}
}

// renderList writes the list template to w with the given status code.
func renderList(w http.ResponseWriter, data listPageData, code int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := listTmpl.ExecuteTemplate(w, "layout", data); err != nil {
		// Headers already sent; log only.
		_ = err
	}
}

// overrideQueryParam returns a URL-encoded query string identical to existing
// but with key set to val. Any prior values for key are replaced.
func overrideQueryParam(existing url.Values, key, val string) string {
	out := make(url.Values)
	for k, vs := range existing {
		if k == key {
			continue
		}
		out[k] = vs
	}
	out.Set(key, val)
	return out.Encode()
}

// rootView wraps a CapturedRequest for the detail template.
type rootView struct {
	*capture.CapturedRequest
}

// HeadersJSON serialises the request headers as a JSON string for the
// Copy-as-cURL data attribute.
func (v rootView) HeadersJSON() string {
	b, _ := json.Marshal(v.Headers)
	return string(b)
}

// BodyBase64 returns the body as a base64 string for the Copy-as-cURL data
// attribute. Returns an empty string when the body is empty.
func (v rootView) BodyBase64() string {
	if len(v.Body) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(v.Body)
}

// BodyText returns the body as a human-readable string, pretty-printed when the
// content-type indicates JSON.
func (v rootView) BodyText() string {
	return formatBody(v.Body, v.ContentType)
}

// IsJSON reports whether the request body is JSON.
func (v rootView) IsJSON() bool {
	return isJSONContentType(v.ContentType)
}

// BodyOriginalSizeLabel returns the body size for display. When BodyTruncated
// is true, the value is a lower bound and is prefixed with "≥".
func (v rootView) BodyOriginalSizeLabel() string {
	if v.BodyTruncated {
		return fmt.Sprintf("≥ %d", v.BodyOriginalSize)
	}
	return fmt.Sprintf("%d", v.BodyOriginalSize)
}

// eventView wraps any event record (ResponseEvent or OutboundEvent) for the
// detail template. Methods are dispatched by type assertion on .record.
type eventView struct {
	record any
}

func (ev eventView) Kind() string {
	switch r := ev.record.(type) {
	case *capture.ResponseEvent:
		return string(r.Kind())
	case *capture.OutboundEvent:
		return string(r.Kind())
	}
	return ""
}

func (ev eventView) Timestamp() time.Time {
	switch r := ev.record.(type) {
	case *capture.ResponseEvent:
		return r.Timestamp
	case *capture.OutboundEvent:
		return r.Timestamp
	}
	return time.Time{}
}

func (ev eventView) DurationMS() int64 {
	switch r := ev.record.(type) {
	case *capture.ResponseEvent:
		return r.DurationMS
	case *capture.OutboundEvent:
		return r.DurationMS
	}
	return 0
}

// ResponseEvent field accessors.

func (ev eventView) Status() int {
	if r, ok := ev.record.(*capture.ResponseEvent); ok {
		return r.Status
	}
	return 0
}

func (ev eventView) StatusClass() string {
	return httpStatusClass(ev.Status())
}

func (ev eventView) Headers() map[string][]string {
	if r, ok := ev.record.(*capture.ResponseEvent); ok {
		return r.Headers
	}
	return nil
}

func (ev eventView) Body() []byte {
	if r, ok := ev.record.(*capture.ResponseEvent); ok {
		return r.Body
	}
	return nil
}

func (ev eventView) BodyText() string {
	if r, ok := ev.record.(*capture.ResponseEvent); ok {
		return formatBody(r.Body, r.ContentType)
	}
	return ""
}

func (ev eventView) IsJSON() bool {
	if r, ok := ev.record.(*capture.ResponseEvent); ok {
		return isJSONContentType(r.ContentType)
	}
	return false
}

func (ev eventView) BodyTruncated() bool {
	if r, ok := ev.record.(*capture.ResponseEvent); ok {
		return r.BodyTruncated
	}
	return false
}

func (ev eventView) BodyOriginalSize() int {
	if r, ok := ev.record.(*capture.ResponseEvent); ok {
		return r.BodyOriginalSize
	}
	return 0
}

// BodyOriginalSizeLabel returns the body size for display. When BodyTruncated
// is true, the value is a lower bound and is prefixed with "≥".
func (ev eventView) BodyOriginalSizeLabel() string {
	if ev.BodyTruncated() {
		return fmt.Sprintf("≥ %d", ev.BodyOriginalSize())
	}
	return fmt.Sprintf("%d", ev.BodyOriginalSize())
}

// OutboundEvent field accessors.

func (ev eventView) HasOutboundResponse() bool {
	if r, ok := ev.record.(*capture.OutboundEvent); ok {
		return r.Response != nil
	}
	return false
}

func (ev eventView) OutboundMethod() string {
	if r, ok := ev.record.(*capture.OutboundEvent); ok {
		return r.Request.Method
	}
	return ""
}

func (ev eventView) OutboundPath() string {
	if r, ok := ev.record.(*capture.OutboundEvent); ok {
		return r.Request.Path
	}
	return ""
}

func (ev eventView) OutboundRequestHeaders() map[string][]string {
	if r, ok := ev.record.(*capture.OutboundEvent); ok {
		return r.Request.Headers
	}
	return nil
}

func (ev eventView) OutboundRequestBody() []byte {
	if r, ok := ev.record.(*capture.OutboundEvent); ok {
		return r.Request.Body
	}
	return nil
}

func (ev eventView) OutboundRequestBodyText() string {
	if r, ok := ev.record.(*capture.OutboundEvent); ok {
		return formatBody(r.Request.Body, r.Request.ContentType)
	}
	return ""
}

func (ev eventView) OutboundRequestIsJSON() bool {
	if r, ok := ev.record.(*capture.OutboundEvent); ok {
		return isJSONContentType(r.Request.ContentType)
	}
	return false
}

func (ev eventView) OutboundRequestBodyTruncated() bool {
	if r, ok := ev.record.(*capture.OutboundEvent); ok {
		return r.Request.BodyTruncated
	}
	return false
}

func (ev eventView) OutboundRequestBodyOriginalSize() int {
	if r, ok := ev.record.(*capture.OutboundEvent); ok {
		return r.Request.BodyOriginalSize
	}
	return 0
}

// OutboundRequestBodyOriginalSizeLabel returns the outbound request body size
// for display. When BodyTruncated is true, the value is a lower bound and is
// prefixed with "≥".
func (ev eventView) OutboundRequestBodyOriginalSizeLabel() string {
	if ev.OutboundRequestBodyTruncated() {
		return fmt.Sprintf("≥ %d", ev.OutboundRequestBodyOriginalSize())
	}
	return fmt.Sprintf("%d", ev.OutboundRequestBodyOriginalSize())
}

func (ev eventView) OutboundStatus() int {
	if r, ok := ev.record.(*capture.OutboundEvent); ok && r.Response != nil {
		return r.Response.Status
	}
	return 0
}

func (ev eventView) OutboundStatusClass() string {
	return httpStatusClass(ev.OutboundStatus())
}

func (ev eventView) OutboundResponseHeaders() map[string][]string {
	if r, ok := ev.record.(*capture.OutboundEvent); ok && r.Response != nil {
		return r.Response.Headers
	}
	return nil
}

func (ev eventView) OutboundResponseBody() []byte {
	if r, ok := ev.record.(*capture.OutboundEvent); ok && r.Response != nil {
		return r.Response.Body
	}
	return nil
}

func (ev eventView) OutboundResponseBodyText() string {
	if r, ok := ev.record.(*capture.OutboundEvent); ok && r.Response != nil {
		return formatBody(r.Response.Body, r.Response.ContentType)
	}
	return ""
}

func (ev eventView) OutboundResponseIsJSON() bool {
	if r, ok := ev.record.(*capture.OutboundEvent); ok && r.Response != nil {
		return isJSONContentType(r.Response.ContentType)
	}
	return false
}

func (ev eventView) OutboundResponseBodyTruncated() bool {
	if r, ok := ev.record.(*capture.OutboundEvent); ok && r.Response != nil {
		return r.Response.BodyTruncated
	}
	return false
}

func (ev eventView) OutboundResponseBodyOriginalSize() int {
	if r, ok := ev.record.(*capture.OutboundEvent); ok && r.Response != nil {
		return r.Response.BodyOriginalSize
	}
	return 0
}

// OutboundResponseBodyOriginalSizeLabel returns the outbound response body size
// for display. When BodyTruncated is true, the value is a lower bound and is
// prefixed with "≥".
func (ev eventView) OutboundResponseBodyOriginalSizeLabel() string {
	if ev.OutboundResponseBodyTruncated() {
		return fmt.Sprintf("≥ %d", ev.OutboundResponseBodyOriginalSize())
	}
	return fmt.Sprintf("%d", ev.OutboundResponseBodyOriginalSize())
}

// Common accessors for event-as-root rendering.

func (ev eventView) Service() string {
	switch r := ev.record.(type) {
	case *capture.ResponseEvent:
		return r.Service
	case *capture.OutboundEvent:
		return r.Service
	}
	return ""
}

func (ev eventView) ServiceSource() string {
	switch r := ev.record.(type) {
	case *capture.ResponseEvent:
		return r.ServiceSource
	case *capture.OutboundEvent:
		return r.ServiceSource
	}
	return ""
}

func (ev eventView) CorrelationID() string {
	switch r := ev.record.(type) {
	case *capture.ResponseEvent:
		return r.CorrelationID
	case *capture.OutboundEvent:
		return r.CorrelationID
	}
	return ""
}

func (ev eventView) CorrelationSource() string {
	switch r := ev.record.(type) {
	case *capture.ResponseEvent:
		return r.CorrelationSource
	case *capture.OutboundEvent:
		return r.CorrelationSource
	}
	return ""
}

// IsOrphan reports whether the event has no sibling CapturedRequest in the
// correlated set. It is used to label the root event as "orphan" or not.
// The caller passes in whether a CapturedRequest sibling was found.
func (ev eventView) IsResponseEvent() bool {
	_, ok := ev.record.(*capture.ResponseEvent)
	return ok
}

func (ev eventView) IsOutboundEvent() bool {
	_, ok := ev.record.(*capture.OutboundEvent)
	return ok
}

// eventDetailPageData is the template data for GET /ui/requests/{id} when the
// resolved root is a ResponseEvent or OutboundEvent rather than a CapturedRequest.
type eventDetailPageData struct {
	Page              string
	NotFound          bool
	ID                string
	Root              eventView
	Events            []eventView
	HasRequestSibling bool
}

// detailPageData is the template data for GET /ui/requests/{id}.
type detailPageData struct {
	Page     string
	NotFound bool
	ID       string
	Root     rootView
	Events   []eventView
}

// FirstResponse returns the first ResponseEvent in Events, or nil when none.
// Used by the Response tab in the detail template.
func (d detailPageData) FirstResponse() *eventView {
	for i, ev := range d.Events {
		if ev.IsResponseEvent() {
			return &d.Events[i]
		}
	}
	return nil
}

// requestDetailUIHandler returns an http.HandlerFunc for GET /ui/requests/{id}.
func requestDetailUIHandler(rs ReadSources) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		ctx, cancel := withInspectTimeout(r.Context(), rs.QueryTimeout)
		defer cancel()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")

		if rs.Memory == nil && rs.SQLite == nil {
			renderDetailNotFound(w, id)
			return
		}

		detail, err := gatherDetail(ctx, id, rs.Memory, rs.SQLite)
		if errors.Is(err, inspect.ErrNotFound) {
			renderDetailNotFound(w, id)
			return
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("read error: %v", err), http.StatusInternalServerError)
			return
		}

		switch root := detail.Root.(type) {
		case *capture.CapturedRequest:
			data := detailPageData{Page: "detail", Root: rootView{root}}
			for _, e := range detail.Events {
				data.Events = append(data.Events, eventView{record: e})
			}
			w.WriteHeader(http.StatusOK)
			_ = detailTmpl.ExecuteTemplate(w, "layout", data)

		case *capture.ResponseEvent, *capture.OutboundEvent:
			var hasReq bool
			var siblings []eventView
			for _, e := range detail.Events {
				if _, ok := e.(*capture.CapturedRequest); ok {
					hasReq = true
				} else {
					siblings = append(siblings, eventView{record: e})
				}
			}
			data := eventDetailPageData{
				Page:              "detail",
				Root:              eventView{record: detail.Root},
				Events:            siblings,
				HasRequestSibling: hasReq,
			}
			w.WriteHeader(http.StatusOK)
			_ = eventDetailTmpl.ExecuteTemplate(w, "layout", data)

		default:
			renderDetailNotFound(w, id)
		}
	}
}

// renderDetailNotFound writes the 404 HTML page using the detail template.
func renderDetailNotFound(w http.ResponseWriter, id string) {
	data := detailPageData{Page: "detail", NotFound: true, ID: id}
	w.WriteHeader(http.StatusNotFound)
	_ = detailTmpl.ExecuteTemplate(w, "layout", data)
}

// servicesPageData is the template data for GET /ui/services.
type servicesPageData struct {
	Page     string
	Error    string
	Services []serviceCard
}

// serviceCard is the rendered view-model for one service on the Services page.
type serviceCard struct {
	Name      string
	Requests  int
	LastSeen  string // relative ("2m ago"); empty when never seen
	Responses int    // total correlated responses across all classes
	Segments  []statusSegment
}

// statusSegment is one class slice of a service's status-mix bar.
type statusSegment struct {
	Class string  // "2xx".."5xx" or "other"
	Count int
	Pct   float64 // width percentage, 0..100
}

// servicesUIHandler returns an http.HandlerFunc for GET /ui/services.
// It lists the services seen by either reader over the last `servicesSince`
// window with per-service request count, last-seen, and status mix.
func servicesUIHandler(rs ReadSources) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := withInspectTimeout(r.Context(), rs.QueryTimeout)
		defer cancel()
		data := servicesPageData{Page: "services"}

		rd := rs.Memory
		if rd == nil {
			rd = rs.SQLite
		}
		if rd != nil {
			now := time.Now()
			stats, err := rd.ServiceStats(ctx, now.Add(-servicesSince))
			if err != nil {
				data.Error = fmt.Sprintf("read error: %v", err)
			} else {
				data.Services = make([]serviceCard, 0, len(stats))
				for _, st := range stats {
					data.Services = append(data.Services, buildServiceCard(st, now))
				}
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_ = servicesTmpl.ExecuteTemplate(w, "layout", data)
	}
}

// buildServiceCard derives the Services-page view-model from a ServiceStat,
// computing the status-mix segments and a relative last-seen string.
func buildServiceCard(st inspect.ServiceStat, now time.Time) serviceCard {
	card := serviceCard{
		Name:     st.Name,
		Requests: st.Requests,
		LastSeen: relativeTime(st.LastSeen, now),
	}
	classes := []struct {
		name  string
		count int
	}{
		{"2xx", st.S2xx}, {"3xx", st.S3xx}, {"4xx", st.S4xx},
		{"5xx", st.S5xx}, {"other", st.Other},
	}
	card.Responses = st.S2xx + st.S3xx + st.S4xx + st.S5xx + st.Other
	if card.Responses > 0 {
		for _, c := range classes {
			if c.count == 0 {
				continue
			}
			card.Segments = append(card.Segments, statusSegment{
				Class: c.name,
				Count: c.count,
				Pct:   float64(c.count) / float64(card.Responses) * 100,
			})
		}
	}
	return card
}

// relativeTime renders the gap between t and now as a coarse human string.
// A zero t yields the empty string.
func relativeTime(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// httpStatusClass maps an HTTP status code to a CSS class suffix.
func httpStatusClass(code int) string {
	switch {
	case code >= 200 && code < 300:
		return "2xx"
	case code >= 300 && code < 400:
		return "3xx"
	case code >= 400 && code < 500:
		return "4xx"
	case code >= 500 && code < 600:
		return "5xx"
	default:
		return "other"
	}
}

// isJSONContentType reports whether the content-type header value indicates JSON.
func isJSONContentType(ct string) bool {
	return strings.Contains(strings.ToLower(ct), "application/json")
}

// formatBody formats body bytes for display. JSON bodies are pretty-printed;
// all others are returned as plain strings.
func formatBody(body []byte, contentType string) string {
	if len(body) == 0 {
		return ""
	}
	if isJSONContentType(contentType) {
		var buf bytes.Buffer
		if err := json.Indent(&buf, body, "", "  "); err == nil {
			return buf.String()
		}
	}
	return string(body)
}

// configurationPageData is the template data for GET /ui/configuration.
type configurationPageData struct {
	Page       string
	YAML       template.HTML
	Unredacted bool
}

const (
	adminTokenMaskUnset = "<unset>"
	adminTokenMaskSet   = "<set>"
)

// configurationAnchorKey matches a top-level YAML key at column zero. The
// captured name becomes an in-page anchor so chip links like
// /ui/configuration#redaction land on the matching section header.
var configurationAnchorKey = regexp.MustCompile(`(?m)^([a-z_]+):`)

// configurationUIHandler precomputes the rendered page body and an ETag at
// construction time. The effective config is immutable for the lifetime of the
// process (rule reload requires restart), so per-request work collapses to a
// conditional-GET check and a byte write — matching the static-asset pattern.
func configurationUIHandler(effective config.Config, unredacted func() bool) http.HandlerFunc {
	display := effective
	if display.Admin.Token == "" {
		display.Admin.Token = adminTokenMaskUnset
	} else {
		display.Admin.Token = adminTokenMaskSet
	}

	var yamlBuf bytes.Buffer
	enc := yaml.NewEncoder(&yamlBuf)
	enc.SetIndent(2)
	if err := enc.Encode(display); err != nil {
		panic(fmt.Errorf("configuration page: marshal config: %w", err))
	}
	_ = enc.Close()

	data := configurationPageData{
		Page:       "configuration",
		YAML:       template.HTML(injectConfigurationAnchors(yamlBuf.String())),
		Unredacted: unredacted(),
	}

	var bodyBuf bytes.Buffer
	if err := configurationTmpl.ExecuteTemplate(&bodyBuf, "layout", data); err != nil {
		panic(fmt.Errorf("configuration page: render template: %w", err))
	}
	body := bodyBuf.Bytes()
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:])[:16] + `"`

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

// injectConfigurationAnchors html-escapes the YAML text, then wraps each
// top-level key in a <span id="key"> so fragment links land on the right line.
// Keys are ASCII identifiers and survive html-escaping unchanged.
func injectConfigurationAnchors(yamlText string) string {
	escaped := template.HTMLEscapeString(yamlText)
	return configurationAnchorKey.ReplaceAllStringFunc(escaped, func(m string) string {
		key := m[:len(m)-1]
		return `<span id="` + key + `">` + key + `</span>:`
	})
}
