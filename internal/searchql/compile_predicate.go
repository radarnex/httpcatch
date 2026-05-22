package searchql

import (
	"bytes"
	"strings"

	"github.com/radarnex/httpcatch/internal/capture"
)

// Predicate is the in-memory matcher emitted by CompilePredicate. It returns
// true when r matches every term in the source Query.
type Predicate func(r *capture.CapturedRequest) bool

// CompilePredicate produces an in-memory matcher that evaluates each term in
// q against a CapturedRequest. Body needles are pre-lowercased once here, not
// per record — scanned dimensions (body, headers) match case-insensitively to
// mirror the SQLite reader's LOWER(...) LIKE predicates. Freeform terms
// (Field == "") match against the Tier-1 fields available on a CapturedRequest
// (host, path, service, body, headers); correlated event fields contribute
// via the memory reader's orphan-event matching, not this predicate.
func CompilePredicate(q Query) Predicate {
	if len(q.Terms) == 0 {
		return func(*capture.CapturedRequest) bool { return true }
	}
	terms := q.Terms
	bodyNeedles := make([][]byte, len(terms))
	for i, t := range terms {
		if t.Field == FieldBody || t.Field == "" {
			bodyNeedles[i] = bytes.ToLower([]byte(t.Value))
		}
	}
	return func(r *capture.CapturedRequest) bool {
		for i := range terms {
			if !matchTerm(&terms[i], bodyNeedles[i], r) {
				return false
			}
		}
		return true
	}
}

func matchTerm(t *Term, bodyNeedle []byte, r *capture.CapturedRequest) bool {
	if t.Field == FieldStatus {
		// status depends on the events join the memory reader does not
		// perform; the predicate accepts and lets the reader filter, whether
		// the term is positive or negated.
		return true
	}
	matched := evaluateTerm(t, bodyNeedle, r)
	if t.Negated {
		return !matched
	}
	return matched
}

func evaluateTerm(t *Term, bodyNeedle []byte, r *capture.CapturedRequest) bool {
	switch t.Field {
	case "":
		return MatchFreeformRequest(t, bodyNeedle, r)
	case FieldHost:
		return matchString(hostOf(r), t)
	case FieldPath:
		return matchString(r.Path, t)
	case FieldService:
		return matchString(r.Service, t)
	case FieldBody:
		return bodyContainsFold(r.Body, bodyNeedle)
	case FieldHeaders:
		return HeadersAny(r.Headers, t.Value)
	case FieldHeader:
		return HeaderValueContains(r.Headers, t.HeaderName, t.Value)
	case FieldMethod:
		return r.Method == t.Value
	case FieldSourceIP:
		return r.SourceIP == t.Value
	case FieldCorrelationID:
		return r.CorrelationID == t.Value
	}
	return false
}

// bodyContainsFold reports whether body contains needle case-insensitively.
// needle must already be lowercased by the caller. Mirrors the SQLite reader's
// LOWER(...) LIKE for scanned text/blob columns.
func bodyContainsFold(body, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	return bytes.Contains(bytes.ToLower(body), needle)
}

// MatchFreeformRequest reports whether t matches any of the Tier-1 fields on a
// CapturedRequest: host, path, service (indexed dims honour the wildcard
// shape), body (case-insensitive substring), or any header name/value pair
// (case-insensitive substring). Method, status, source_ip, and correlation_id
// are excluded by design — typing the value of one of those does not return
// matching rows.
func MatchFreeformRequest(t *Term, bodyNeedle []byte, r *capture.CapturedRequest) bool {
	if matchString(hostOf(r), t) {
		return true
	}
	if matchString(r.Path, t) {
		return true
	}
	if matchString(r.Service, t) {
		return true
	}
	if bodyContainsFold(r.Body, bodyNeedle) {
		return true
	}
	if HeadersAny(r.Headers, t.Value) {
		return true
	}
	return false
}

// MatchFreeformResponseEvent reports whether t matches any Tier-1 field on a
// ResponseEvent treated as a root: service (indexed wildcard) or any
// header/body case-insensitive substring.
func MatchFreeformResponseEvent(t *Term, ev *capture.ResponseEvent) bool {
	lowerVal := []byte(strings.ToLower(t.Value))
	if matchString(ev.Service, t) {
		return true
	}
	if bodyContainsFold(ev.Body, lowerVal) {
		return true
	}
	if HeadersAny(ev.Headers, t.Value) {
		return true
	}
	return false
}

// MatchFreeformOutboundEvent reports whether t matches any Tier-1 field on an
// OutboundEvent treated as a root: service (indexed wildcard), the request
// path (indexed wildcard), or any header/body case-insensitive substring across
// the request and (when present) response halves.
func MatchFreeformOutboundEvent(t *Term, ev *capture.OutboundEvent) bool {
	lowerVal := []byte(strings.ToLower(t.Value))
	if matchString(ev.Service, t) {
		return true
	}
	if matchString(ev.Request.Path, t) {
		return true
	}
	if bodyContainsFold(ev.Request.Body, lowerVal) {
		return true
	}
	if HeadersAny(ev.Request.Headers, t.Value) {
		return true
	}
	if ev.Response != nil {
		if bodyContainsFold(ev.Response.Body, lowerVal) {
			return true
		}
		if HeadersAny(ev.Response.Headers, t.Value) {
			return true
		}
	}
	return false
}

// HeadersAny reports whether any (name, value) pair in h substring-matches
// needle, case-insensitively. Mirrors the SQLite reader's LOWER(...) LIKE on
// the headers JSON column. Exported so the orphan-event arm can apply the
// same predicate against ResponseEvent/OutboundEvent header maps.
func HeadersAny(h map[string][]string, needle string) bool {
	needleLower := strings.ToLower(needle)
	for name, vs := range h {
		if strings.Contains(strings.ToLower(name), needleLower) {
			return true
		}
		for _, v := range vs {
			if strings.Contains(strings.ToLower(v), needleLower) {
				return true
			}
		}
	}
	return false
}

// HeaderValueContains reports whether the canonical-named header's values
// substring-match needle, case-insensitively. A missing header never matches
// a positive predicate; negation is applied by the caller. Exported for the
// orphan-event arm.
func HeaderValueContains(h map[string][]string, name, needle string) bool {
	vs, ok := h[name]
	if !ok {
		return false
	}
	needleLower := strings.ToLower(needle)
	for _, v := range vs {
		if strings.Contains(strings.ToLower(v), needleLower) {
			return true
		}
	}
	return false
}

// matchString applies a term's wildcard shape to a single field value. None is
// exact match; Prefix matches the beginning of s; Substring matches anywhere
// within s. Quoted values arrive with WildcardNone so a literal `*` in the
// value round-trips through exact match.
func matchString(s string, t *Term) bool {
	switch t.Wildcard {
	case WildcardPrefix:
		return strings.HasPrefix(s, t.Value)
	case WildcardSubstring:
		return strings.Contains(s, t.Value)
	default:
		return s == t.Value
	}
}

// hostOf returns the canonical Host header value for a CapturedRequest. The
// capture handler always writes headers via capture.HostHeader, which is the
// canonical "Host", so a single lookup is sufficient.
func hostOf(r *capture.CapturedRequest) string {
	if r == nil {
		return ""
	}
	if vs, ok := r.Headers[capture.HostHeader]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}
