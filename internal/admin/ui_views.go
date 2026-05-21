package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/radarnex/httpcatch/internal/capture"
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

// listTmpl, detailTmpl, and eventDetailTmpl are parsed once at startup from
// the embedded FS. Each is parsed together with layout.html so the
// {{define "body"}} block in each page template overrides the {{block "body"}}
// slot in the layout.
var (
	listTmpl        = template.Must(template.ParseFS(uiFS, "ui/layout.html", "ui/requests_list.html"))
	detailTmpl      = template.Must(template.ParseFS(uiFS, "ui/layout.html", "ui/requests_detail.html"))
	eventDetailTmpl = template.Must(template.ParseFS(uiFS, "ui/layout.html", "ui/events_detail.html"))
	servicesTmpl    = template.Must(template.ParseFS(uiFS, "ui/layout.html", "ui/services.html"))
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
func requestListHandler(memReader, sqlReader inspect.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		data := listPageData{
			Page:    "explorer",
			Methods: httpMethods,
		}

		// Populate the service dropdown from whichever reader is available.
		rd := memReader
		if rd == nil {
			rd = sqlReader
		}
		if rd != nil {
			since := time.Now().Add(-servicesSince)
			if svcs, err := rd.ServicesSeen(ctx, since); err == nil {
				data.Services = svcs
			}
		}

		// Round-trip raw filter strings into the form.
		vals := r.URL.Query()
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

		rows, nextCur, _, err := gatherRoots(ctx, q, memReader, sqlReader)
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
func requestDetailUIHandler(memReader, sqlReader inspect.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		ctx := r.Context()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")

		if memReader == nil && sqlReader == nil {
			renderDetailNotFound(w, id)
			return
		}

		detail, err := gatherDetail(ctx, id, memReader, sqlReader)
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
	Services []string
}

// servicesUIHandler returns an http.HandlerFunc for GET /ui/services.
// It lists the services seen by either reader over the last `servicesSince`
// window, sorted alphabetically.
func servicesUIHandler(memReader, sqlReader inspect.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		data := servicesPageData{Page: "services"}

		rd := memReader
		if rd == nil {
			rd = sqlReader
		}
		if rd != nil {
			since := time.Now().Add(-servicesSince)
			svcs, err := rd.ServicesSeen(ctx, since)
			if err != nil {
				data.Error = fmt.Sprintf("read error: %v", err)
			} else {
				data.Services = svcs
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_ = servicesTmpl.ExecuteTemplate(w, "layout", data)
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
