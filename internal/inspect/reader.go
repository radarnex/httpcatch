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

// StatusFilter holds a parsed status filter — either an exact status code or a
// class (1xx, 2xx, 3xx, 4xx, 5xx). Exactly one of Exact and Class is set.
type StatusFilter struct {
	// Exact is non-zero when the filter is an integer status code (e.g. 200).
	Exact int
	// Class is non-empty when the filter is a class string (e.g. "2xx"). When
	// set, the matching range is [Class*100, Class*100+99].
	Class string
}

// InspectQuery carries the parameters parsed from GET /requests. All filter
// fields compose with AND semantics. Any non-temporal filter (Service, Method,
// Status, Path, CorrelationID, SourceIP, HasEvents) forces SQLite-only reads.
type InspectQuery struct {
	// Pagination.
	Limit  int
	Cursor *Cursor

	// Temporal filters — compatible with memory reads.
	Since *time.Time
	Until *time.Time

	// Non-temporal filters — force SQLite-only reads.
	Service       string
	Method        string
	Status        *StatusFilter
	Path          string
	CorrelationID string
	SourceIP      string
	HasEvents     *bool
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
