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
)

// rootRedirectHandler redirects the root URL to the request list.
// Unauthenticated browsers are caught by the auth middleware first, which sends
// them to /login with ?next=/ui/requests, and they arrive here only after auth.
func rootRedirectHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/requests", http.StatusSeeOther)
	}
}

// listQueryView carries the raw filter strings as entered by the operator.
// These are round-tripped from the URL into the form so the operator sees
// the current filter state.
type listQueryView struct {
	Service   string
	Method    string
	Path      string
	Body      string
	StatusRaw string
	SinceRaw  string
	UntilRaw  string
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

// StatusText returns the formatted status code or an empty string when unknown.
func (r rowView) StatusText() string {
	if r.RootRow.Status == nil {
		return ""
	}
	return fmt.Sprintf("%d", *r.RootRow.Status)
}

// listPageData is the template data for GET /ui/requests.
type listPageData struct {
	Error    string
	Services []string
	Methods  []string
	Query    listQueryView
	Rows     []rowView
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
			Service:   vals.Get("service"),
			Method:    vals.Get("method"),
			Path:      vals.Get("path"),
			Body:      vals.Get("body"),
			StatusRaw: vals.Get("status"),
			SinceRaw:  vals.Get("since"),
			UntilRaw:  vals.Get("until"),
		}

		q, fieldErrs := parseInspectQuery(vals)
		if len(fieldErrs) > 0 {
			data.Error = fieldErrs[0].Error()
			renderList(w, data, http.StatusOK)
			return
		}

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
	NotFound        bool
	ID              string
	Root            eventView
	Events          []eventView
	HasRequestSibling bool
}

// detailPageData is the template data for GET /ui/requests/{id}.
type detailPageData struct {
	NotFound bool
	ID       string
	Root     rootView
	Events   []eventView
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
			data := detailPageData{Root: rootView{root}}
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
	data := detailPageData{NotFound: true, ID: id}
	w.WriteHeader(http.StatusNotFound)
	_ = detailTmpl.ExecuteTemplate(w, "layout", data)
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
