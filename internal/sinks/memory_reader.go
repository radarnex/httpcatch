package sinks

import (
	"context"
	"sort"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
)

// ReadRoots returns captured-request rows from the in-memory ring buffer,
// sorted timestamp DESC, id DESC, paginated by cursor and limited to limit+1
// rows (the extra row determines nextCursor). Only CapturedRequest records are
// returned; other variants are handled by later slices.
func (s *MemorySink) ReadRoots(_ context.Context, _ inspect.InspectQuery, limit int, cursor *inspect.Cursor) ([]inspect.RootRow, *inspect.Cursor, error) {
	all := s.Recent(s.Len())

	// Ensure stable sort by (timestamp DESC, id DESC). The ring traversal is
	// newest-first by insertion order, but inserts can arrive out of wall-clock
	// order, so an explicit sort is required for correctness.
	sort.SliceStable(all, func(i, j int) bool {
		ti := all[i].RecordTimestamp()
		tj := all[j].RecordTimestamp()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return all[i].RecordID() > all[j].RecordID()
	})

	// Apply cursor filter: only rows strictly before the cursor position.
	if cursor != nil {
		filtered := all[:0]
		for _, r := range all {
			ts := r.RecordTimestamp()
			id := r.RecordID()
			if ts.Before(cursor.Timestamp) || (ts.Equal(cursor.Timestamp) && id < cursor.ID) {
				filtered = append(filtered, r)
			}
		}
		all = filtered
	}

	// Collect up to limit+1 rows.
	take := min(limit+1, len(all))
	page := all[:take]

	var nextCursor *inspect.Cursor
	if len(page) > limit {
		last := page[limit-1]
		nextCursor = &inspect.Cursor{
			Timestamp: last.RecordTimestamp(),
			ID:        last.RecordID(),
		}
		page = page[:limit]
	}

	rows := make([]inspect.RootRow, 0, len(page))
	for _, r := range page {
		cr, ok := r.(*capture.CapturedRequest)
		if !ok {
			continue
		}
		rows = append(rows, inspect.RootRow{
			ID:            cr.ID,
			Kind:          "request",
			Timestamp:     cr.Timestamp,
			Service:       cr.Service,
			Method:        cr.Method,
			Path:          cr.Path,
			CorrelationID: cr.CorrelationID,
			SourceIP:      cr.SourceIP,
		})
	}
	return rows, nextCursor, nil
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
