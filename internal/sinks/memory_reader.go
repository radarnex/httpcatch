package sinks

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
	"github.com/radarnex/httpcatch/internal/searchql"
)

// ReadRoots returns captured-request and orphan-event rows from the in-memory
// ring buffer, sorted timestamp DESC, id DESC, paginated by cursor and limited
// to limit+1 rows (the extra row determines nextCursor).
//
// Service, correlation_id, since, and until filters are applied during the emit
// pass. Filters that only apply to captured requests (method, path, source_ip,
// has_events) are routed to SQLite-only by the caller; when any of those is set,
// orphans are omitted from the result.
//
// Orphan detection: a response or outbound event whose correlation_id has no
// corresponding CapturedRequest in the ring buffer is an orphan. Two passes over
// the snapshot: one to build the request-correlation set, one to emit rows.
func (s *MemorySink) ReadRoots(_ context.Context, q inspect.InspectQuery, limit int, cursor *inspect.Cursor) ([]inspect.RootRow, *inspect.Cursor, error) {
	all := s.Recent(s.Len())

	// Build the set of correlation_ids that have at least one CapturedRequest
	// in the buffer (used for orphan detection) and count correlated events
	// (ResponseEvent and OutboundEvent) per correlation_id.
	requestCorrs := make(map[string]struct{}, len(all))
	eventCounts := make(map[string]int, len(all))
	for _, r := range all {
		switch r.(type) {
		case *capture.CapturedRequest:
			requestCorrs[r.RecordCorrelationID()] = struct{}{}
		case *capture.ResponseEvent, *capture.OutboundEvent:
			eventCounts[r.RecordCorrelationID()]++
		}
	}

	includeOrphans := !q.HasRequestOnlyFilter()
	matchRequest := searchql.CompilePredicate(q.Query)

	// Emit a RootRow for each eligible record, applying field-level filters
	// that do not depend on sort order. Temporal filters and field-level terms
	// are applied here to avoid appending rows that will be dropped later.
	candidates := make([]inspect.RootRow, 0, len(all))
	for _, r := range all {
		// Temporal filters apply to all record types on the record's own timestamp.
		ts := r.RecordTimestamp()
		if q.Since != nil && ts.Before(*q.Since) {
			continue
		}
		if q.Until != nil && !ts.Before(*q.Until) {
			continue
		}

		switch v := r.(type) {
		case *capture.CapturedRequest:
			if !matchRequest(v) {
				continue
			}
			ec := eventCounts[v.CorrelationID]
			he := ec > 0
			candidates = append(candidates, inspect.RootRow{
				ID:            v.ID,
				Kind:          "request",
				Timestamp:     v.Timestamp,
				Service:       v.Service,
				Method:        v.Method,
				Path:          v.Path,
				CorrelationID: v.CorrelationID,
				SourceIP:      v.SourceIP,
				EventCount:    &ec,
				HasEvents:     &he,
			})
		case *capture.ResponseEvent:
			if !includeOrphans {
				continue
			}
			if _, hasReq := requestCorrs[v.CorrelationID]; hasReq {
				continue // correlated with a known request, not an orphan
			}
			if !matchOrphanResponse(q.Query, v) {
				continue
			}
			st := v.Status
			candidates = append(candidates, inspect.RootRow{
				ID:            v.ID,
				Kind:          "orphan_response",
				Timestamp:     v.Timestamp,
				Service:       v.Service,
				CorrelationID: v.CorrelationID,
				Status:        &st,
			})
		case *capture.OutboundEvent:
			if !includeOrphans {
				continue
			}
			if _, hasReq := requestCorrs[v.CorrelationID]; hasReq {
				continue
			}
			if !matchOrphanOutbound(q.Query, v) {
				continue
			}
			candidates = append(candidates, inspect.RootRow{
				ID:            v.ID,
				Kind:          "orphan_outbound",
				Timestamp:     v.Timestamp,
				Service:       v.Service,
				CorrelationID: v.CorrelationID,
			})
		}
	}

	// Ensure stable sort by (timestamp DESC, id DESC).
	sort.SliceStable(candidates, func(i, j int) bool {
		ti := candidates[i].Timestamp
		tj := candidates[j].Timestamp
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return candidates[i].ID > candidates[j].ID
	})

	// Apply cursor filter: only rows strictly before the cursor position.
	if cursor != nil {
		filtered := candidates[:0]
		for _, row := range candidates {
			if row.Timestamp.Before(cursor.Timestamp) ||
				(row.Timestamp.Equal(cursor.Timestamp) && row.ID < cursor.ID) {
				filtered = append(filtered, row)
			}
		}
		candidates = filtered
	}

	// Collect up to limit+1 rows.
	take := min(limit+1, len(candidates))
	page := candidates[:take]

	var nextCursor *inspect.Cursor
	if len(page) > limit {
		last := page[limit-1]
		nextCursor = &inspect.Cursor{
			Timestamp: last.Timestamp,
			ID:        last.ID,
		}
		page = page[:limit]
	}

	return page, nextCursor, nil
}

// ReadDetail resolves the given id in the ring buffer. It first scans for the
// id among all records; if found, it gathers every other record sharing the
// same correlation_id as siblings. Returns ErrNotFound when the id is absent.
func (s *MemorySink) ReadDetail(_ context.Context, id string) (inspect.DetailRecord, error) {
	all := s.Recent(s.Len())

	// Find the root record by id.
	var root capture.Record
	for _, r := range all {
		if r.RecordID() == id {
			root = r
			break
		}
	}
	if root == nil {
		return inspect.DetailRecord{}, inspect.ErrNotFound
	}

	corrID := root.RecordCorrelationID()

	// Gather siblings: every record sharing the correlation_id except the root
	// itself, sorted by timestamp ascending.
	var siblings []capture.Record
	for _, r := range all {
		if r.RecordID() != id && r.RecordCorrelationID() == corrID {
			siblings = append(siblings, r)
		}
	}
	sort.SliceStable(siblings, func(i, j int) bool {
		ti := siblings[i].RecordTimestamp()
		tj := siblings[j].RecordTimestamp()
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return siblings[i].RecordID() < siblings[j].RecordID()
	})

	events := make([]any, len(siblings))
	for i, r := range siblings {
		events[i] = r
	}
	return inspect.DetailRecord{Root: root, Events: events}, nil
}

// AggregateRoots returns the total matching row count and a status-class
// histogram. The ring buffer has no request↔response join, so captured-request
// rows are recorded with no status (folded into Other); orphan response rows
// contribute their own status.
func (s *MemorySink) AggregateRoots(_ context.Context, q inspect.InspectQuery, bucketCount int) (inspect.Aggregation, error) {
	all := s.Recent(s.Len())

	requestCorrs := make(map[string]struct{}, len(all))
	for _, r := range all {
		if _, ok := r.(*capture.CapturedRequest); ok {
			requestCorrs[r.RecordCorrelationID()] = struct{}{}
		}
	}

	includeOrphans := !q.HasRequestOnlyFilter()
	matchRequest := searchql.CompilePredicate(q.Query)

	bucketing := q.Since != nil && q.Until != nil && bucketCount > 0 && q.Until.After(*q.Since)
	var sinceNS, bucketWidth int64
	var buckets []inspect.HistogramBucket
	if bucketing {
		sinceNS = q.Since.UnixNano()
		bucketWidth = (q.Until.UnixNano() - sinceNS) / int64(bucketCount)
		if bucketWidth <= 0 {
			bucketWidth = 1
		}
		buckets = make([]inspect.HistogramBucket, bucketCount)
		for i := range buckets {
			buckets[i].Start = time.Unix(0, sinceNS+bucketWidth*int64(i)).UTC()
		}
	}

	tally := func(ts time.Time, status *int) {
		if !bucketing {
			return
		}
		idx := int((ts.UnixNano() - sinceNS) / bucketWidth)
		if idx < 0 {
			idx = 0
		}
		if idx >= bucketCount {
			idx = bucketCount - 1
		}
		switch statusClass(status) {
		case 2:
			buckets[idx].S2xx++
		case 3:
			buckets[idx].S3xx++
		case 4:
			buckets[idx].S4xx++
		case 5:
			buckets[idx].S5xx++
		default:
			buckets[idx].Other++
		}
	}

	var total int
	for _, r := range all {
		ts := r.RecordTimestamp()
		if q.Since != nil && ts.Before(*q.Since) {
			continue
		}
		if q.Until != nil && !ts.Before(*q.Until) {
			continue
		}

		switch v := r.(type) {
		case *capture.CapturedRequest:
			if !matchRequest(v) {
				continue
			}
			total++
			tally(ts, nil)
		case *capture.ResponseEvent:
			if !includeOrphans {
				continue
			}
			if _, hasReq := requestCorrs[v.CorrelationID]; hasReq {
				continue
			}
			if !matchOrphanResponse(q.Query, v) {
				continue
			}
			st := v.Status
			total++
			tally(ts, &st)
		case *capture.OutboundEvent:
			if !includeOrphans {
				continue
			}
			if _, hasReq := requestCorrs[v.CorrelationID]; hasReq {
				continue
			}
			if !matchOrphanOutbound(q.Query, v) {
				continue
			}
			total++
			tally(ts, nil)
		}
	}

	return inspect.Aggregation{Total: total, Buckets: buckets}, nil
}

func statusClass(s *int) int {
	if s == nil {
		return 0
	}
	v := *s
	switch {
	case v >= 200 && v < 300:
		return 2
	case v >= 300 && v < 400:
		return 3
	case v >= 400 && v < 500:
		return 4
	case v >= 500 && v < 600:
		return 5
	}
	return 0
}

// ServicesSeen returns the distinct services present in the ring buffer that
// were written at or after since (zero means all time), ordered alphabetically.
func (s *MemorySink) ServicesSeen(_ context.Context, since time.Time) ([]string, error) {
	all := s.Recent(s.Len())

	seen := make(map[string]struct{})
	for _, r := range all {
		if !since.IsZero() && r.RecordTimestamp().Before(since) {
			continue
		}
		svc := r.RecordService()
		if svc != "" {
			seen[svc] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for svc := range seen {
		out = append(out, svc)
	}
	sort.Strings(out)
	return out, nil
}

// ServiceStats aggregates per-service activity over the records held in the
// ring buffer. Inbound captured requests drive the request count; correlated
// response events drive the status-class mix; LastSeen tracks the latest
// record of any kind. Results are ordered alphabetically by service name.
func (s *MemorySink) ServiceStats(_ context.Context, since time.Time) ([]inspect.ServiceStat, error) {
	all := s.Recent(s.Len())

	stats := make(map[string]*inspect.ServiceStat)
	get := func(svc string) *inspect.ServiceStat {
		st := stats[svc]
		if st == nil {
			st = &inspect.ServiceStat{Name: svc}
			stats[svc] = st
		}
		return st
	}

	for _, r := range all {
		if !since.IsZero() && r.RecordTimestamp().Before(since) {
			continue
		}
		svc := r.RecordService()
		if svc == "" {
			continue
		}
		st := get(svc)
		if ts := r.RecordTimestamp(); ts.After(st.LastSeen) {
			st.LastSeen = ts
		}
		switch rec := r.(type) {
		case *capture.CapturedRequest:
			st.Requests++
		case *capture.ResponseEvent:
			addStatusClass(st, rec.Status)
		}
	}

	out := make([]inspect.ServiceStat, 0, len(stats))
	for _, st := range stats {
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// addStatusClass increments the status-class counter on st matching code.
func addStatusClass(st *inspect.ServiceStat, code int) {
	switch statusClass(&code) {
	case 2:
		st.S2xx++
	case 3:
		st.S3xx++
	case 4:
		st.S4xx++
	case 5:
		st.S5xx++
	default:
		st.Other++
	}
}

// matchOrphanResponse evaluates the subset of q that applies to orphan
// response events on their own fields: service, correlation_id, status.
// Request-only terms (method, path, source_ip, body, host) bypass this branch
// entirely because the caller has already gated on q.HasRequestOnlyFilter().
func matchOrphanResponse(q searchql.Query, ev *capture.ResponseEvent) bool {
	for _, t := range q.Terms {
		matched, applies := evaluateOrphanResponseTerm(&t, ev)
		if !applies {
			continue
		}
		if t.Negated {
			matched = !matched
		}
		if !matched {
			return false
		}
	}
	return true
}

// matchOrphanOutbound evaluates q against an orphan outbound event. Outbound
// events have no top-level status, so any status term — positive or negated —
// excludes them.
func matchOrphanOutbound(q searchql.Query, ev *capture.OutboundEvent) bool {
	for _, t := range q.Terms {
		if t.Field == searchql.FieldStatus {
			return false
		}
		matched, applies := evaluateOutboundOrphanTerm(&t, ev)
		if !applies {
			continue
		}
		if t.Negated {
			matched = !matched
		}
		if !matched {
			return false
		}
	}
	return true
}

// evaluateOutboundOrphanTerm dispatches per-term evaluation for orphan
// outbound events. Headers terms scan both the request and response halves;
// service/correlation_id fall through to the shared helper.
func evaluateOutboundOrphanTerm(t *searchql.Term, ev *capture.OutboundEvent) (matched, applies bool) {
	switch t.Field {
	case "":
		return searchql.MatchFreeformOutboundEvent(t, ev), true
	case searchql.FieldHeaders:
		if searchql.HeadersAny(ev.Request.Headers, t.Value) {
			return true, true
		}
		if ev.Response != nil && searchql.HeadersAny(ev.Response.Headers, t.Value) {
			return true, true
		}
		return false, true
	case searchql.FieldHeader:
		if searchql.HeaderValueContains(ev.Request.Headers, t.HeaderName, t.Value) {
			return true, true
		}
		if ev.Response != nil && searchql.HeaderValueContains(ev.Response.Headers, t.HeaderName, t.Value) {
			return true, true
		}
		return false, true
	}
	return evaluateOrphanCommonTerm(t, ev.Service, ev.CorrelationID)
}

// evaluateOrphanResponseTerm reports whether t matches ev on the term's field,
// and whether the field is one the orphan response arm answers (service,
// correlation_id, status, headers, header.<name>, freeform). Negation is
// applied by the caller.
func evaluateOrphanResponseTerm(t *searchql.Term, ev *capture.ResponseEvent) (matched, applies bool) {
	if t.Field == "" {
		return searchql.MatchFreeformResponseEvent(t, ev), true
	}
	if t.Field == searchql.FieldStatus {
		if t.StatusFilter == nil {
			return false, false
		}
		if t.StatusFilter.Exact != 0 {
			return ev.Status == t.StatusFilter.Exact, true
		}
		lo, hi := t.StatusFilter.ClassRange()
		return ev.Status >= lo && ev.Status <= hi, true
	}
	if t.Field == searchql.FieldHeaders {
		return searchql.HeadersAny(ev.Headers, t.Value), true
	}
	if t.Field == searchql.FieldHeader {
		return searchql.HeaderValueContains(ev.Headers, t.HeaderName, t.Value), true
	}
	return evaluateOrphanCommonTerm(t, ev.Service, ev.CorrelationID)
}

// evaluateOrphanCommonTerm reports whether t matches the service /
// correlation_id portion of an event. service honors the term's wildcard;
// correlation_id is exact-only (parser rejects wildcards on it). Negation is
// applied by the caller.
func evaluateOrphanCommonTerm(t *searchql.Term, service, correlationID string) (matched, applies bool) {
	switch t.Field {
	case searchql.FieldService:
		return matchOrphanString(service, t), true
	case searchql.FieldCorrelationID:
		return correlationID == t.Value, true
	}
	return false, false
}

func matchOrphanString(s string, t *searchql.Term) bool {
	switch t.Wildcard {
	case searchql.WildcardPrefix:
		return strings.HasPrefix(s, t.Value)
	case searchql.WildcardSubstring:
		return strings.Contains(s, t.Value)
	default:
		return s == t.Value
	}
}
