package inspect

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by ReadDetail when the requested id does not match
// any record in this reader.
var ErrNotFound = errors.New("not found")

// RootRow is one entry in the GET /requests list. Every captured-request row
// carries kind "request". Orphan-event rows carry their own kind values.
// The event_count/has_events/status columns are derived from a join against
// the events table; they are zero/false/nil when no events have been written
// for the correlated request.
type RootRow struct {
	ID            string     `json:"id"`
	Kind          string     `json:"kind"`
	Timestamp     time.Time  `json:"timestamp"`
	Service       string     `json:"service"`
	Method        string     `json:"method"`
	Path          string     `json:"path"`
	CorrelationID string     `json:"correlation_id"`
	SourceIP      string     `json:"source_ip"`
	EventCount    int        `json:"event_count"`
	HasEvents     bool       `json:"has_events"`
	Status        *int       `json:"status"` // nullable; populated via events join
}

// DetailRecord is the return type of ReadDetail. Root is the record matched by
// id; Events holds every other record sharing the same correlation_id, ordered
// by timestamp ascending. Events is always non-nil (empty slice when there are
// no siblings).
type DetailRecord struct {
	Root   any   `json:"root"`
	Events []any `json:"events"`
}

// InspectQuery carries the parameters parsed from GET /requests. Additional
// filter fields extend this type without changing the Reader interface
// signature.
type InspectQuery struct {
	Limit  int
	Cursor *Cursor
}

// Reader is the read-side seam implemented by MemorySink and SQLiteSink.
// StdoutSink does not implement Reader. App composition wires whichever
// readers are enabled into the inspect handler.
type Reader interface {
	// ReadRoots returns captured requests and orphan events, already filtered
	// and paginated. nextCursor is nil when there are no further pages.
	ReadRoots(ctx context.Context, q InspectQuery, limit int, cursor *Cursor) (rows []RootRow, nextCursor *Cursor, err error)

	// ReadDetail returns the full record for id plus any correlated records.
	// Returns ErrNotFound when the id does not match any record.
	ReadDetail(ctx context.Context, id string) (DetailRecord, error)

	// ServicesSeen returns the distinct list of services observed since the
	// given time, ordered alphabetically. A zero since means "all time".
	ServicesSeen(ctx context.Context, since time.Time) ([]string, error)
}
