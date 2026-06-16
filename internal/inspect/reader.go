package inspect

import (
	"context"
	"errors"
	"time"

	"github.com/radarnex/httpcatch/internal/searchql"
)

// ErrNotFound is returned by ReadDetail when the requested id does not match
// any record in this reader.
var ErrNotFound = errors.New("not found")

// RootRow is one entry in the GET /requests list. Every captured-request row
// carries kind "request". Orphan-event rows carry kind "orphan_response" or
// "orphan_outbound".
//
// Fields that apply to captured-request rows only (EventCount, HasEvents,
// Method, Path, SourceIP) are null/omitted on orphan rows — orphan rows carry
// the event's own fields instead. Status is populated via the events join for
// request rows and from the event's own status for orphan_response rows.
type RootRow struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Timestamp     time.Time `json:"timestamp"`
	Service       string    `json:"service"`
	Method        string    `json:"method,omitempty"`
	Path          string    `json:"path,omitempty"`
	CorrelationID string    `json:"correlation_id"`
	SourceIP      string    `json:"source_ip,omitempty"`
	EventCount    *int      `json:"event_count"` // null for orphan rows
	HasEvents     *bool     `json:"has_events"`  // null for orphan rows
	Status        *int      `json:"status"`      // nullable; populated via events join or event's own status
}

// DetailRecord is the return type of ReadDetail. Root is the record matched by
// id; Events holds every other record sharing the same correlation_id, ordered
// by timestamp ascending. Events is always non-nil (empty slice when there are
// no siblings).
type DetailRecord struct {
	Root   any   `json:"root"`
	Events []any `json:"events"`
}

// InspectQuery carries the parameters parsed from GET /requests. Pagination
// and time fields are structured. Field-level filters are carried as a
// parsed searchql.Query and applied by the reader-specific compiler.
type InspectQuery struct {
	// Pagination.
	Limit  int
	Cursor *Cursor

	// Temporal filters — compatible with memory reads.
	Since *time.Time
	Until *time.Time

	// Query is the parsed search-language AST. An empty Query has no terms
	// and imposes no field-level filter.
	Query searchql.Query
}

// HasRequestOnlyFilter reports whether the query carries any term whose
// semantics only apply to CapturedRequest rows. Readers use this to decide
// whether to include orphan event rows in the UNION arm of the result.
func (q InspectQuery) HasRequestOnlyFilter() bool {
	return q.Query.HasRequestOnlyTerm()
}

// HistogramBucket aggregates matching rows within a single time window of the
// requests histogram. Start is the bucket's inclusive lower-edge timestamp;
// the upper edge is the next bucket's Start (or the request's Until for the
// last bucket). The S2xx..S5xx fields count rows whose response status falls
// in the named class; Other counts rows with no status (orphan_outbound or
// requests with no response yet).
type HistogramBucket struct {
	Start time.Time `json:"start"`
	S2xx  int       `json:"s2xx"`
	S3xx  int       `json:"s3xx"`
	S4xx  int       `json:"s4xx"`
	S5xx  int       `json:"s5xx"`
	Other int       `json:"other"`
}

// Aggregation summarises the full set of rows matching a query, independent
// of pagination. Buckets is empty unless q.Since and q.Until are both set,
// in which case it has length bucketCount.
type Aggregation struct {
	Total   int               `json:"total"`
	Buckets []HistogramBucket `json:"buckets"`
}

// ServiceStat summarizes one service's activity over a time window. Requests
// counts captured inbound requests; LastSeen is the most recent record of any
// kind carrying the service. The S2xx..S5xx fields count correlated response
// events by status class; Other counts responses with an out-of-range status.
type ServiceStat struct {
	Name     string    `json:"name"`
	Requests int       `json:"requests"`
	LastSeen time.Time `json:"last_seen"`
	S2xx     int       `json:"s2xx"`
	S3xx     int       `json:"s3xx"`
	S4xx     int       `json:"s4xx"`
	S5xx     int       `json:"s5xx"`
	Other    int       `json:"other"`
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

	// ServiceStats returns per-service activity since the given time (zero
	// means all time), ordered alphabetically by name. The set of services
	// matches ServicesSeen over the same window.
	ServiceStats(ctx context.Context, since time.Time) ([]ServiceStat, error)

	// AggregateRoots computes the total matching row count and a per-bucket
	// status-class breakdown over q's time range. bucketCount is the number
	// of histogram buckets to emit; when q.Since or q.Until is nil, only the
	// total is meaningful and Buckets is empty.
	AggregateRoots(ctx context.Context, q InspectQuery, bucketCount int) (Aggregation, error)
}
