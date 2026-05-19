package sinks

import (
	"context"
	"sort"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
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
	// in the buffer. Used for orphan detection.
	requestCorrs := make(map[string]struct{}, len(all))
	for _, r := range all {
		if _, ok := r.(*capture.CapturedRequest); ok {
			requestCorrs[r.RecordCorrelationID()] = struct{}{}
		}
	}

	// Determine whether orphan rows should be included. method, path,
	// source_ip, and has_events are request-only filters; their presence
	// excludes orphans by definition.
	includeOrphans := q.Method == "" && q.Path == "" && q.SourceIP == "" && q.HasEvents == nil

	// Emit a RootRow for each eligible record, applying field-level filters
	// that do not depend on sort order. Service, correlation_id, and temporal
	// filters are applied here to avoid appending rows that will be dropped later.
	var candidates []inspect.RootRow
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
			if q.Service != "" && v.Service != q.Service {
				continue
			}
			if q.CorrelationID != "" && v.CorrelationID != q.CorrelationID {
				continue
			}
			ec := 0 // event_count is unknown in memory; filled by SQLite join
			he := false
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
			if q.Service != "" && v.Service != q.Service {
				continue
			}
			if q.CorrelationID != "" && v.CorrelationID != q.CorrelationID {
				continue
			}
			// Apply status filter to orphan_response on the event's own status.
			if q.Status != nil {
				if q.Status.Exact != 0 && v.Status != q.Status.Exact {
					continue
				}
				if q.Status.Class != "" {
					lo := int(q.Status.Class[0]-'0') * 100
					hi := lo + 99
					if v.Status < lo || v.Status > hi {
						continue
					}
				}
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
			if q.Service != "" && v.Service != q.Service {
				continue
			}
			if q.CorrelationID != "" && v.CorrelationID != q.CorrelationID {
				continue
			}
			// status filter excludes outbound orphans (they have no status at the top level).
			if q.Status != nil {
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
