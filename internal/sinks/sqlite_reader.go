package sinks

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/radarnex/httpcatch/internal/inspect"
)

// sqliteReadRoots is the base query joining captured_requests against events
// for event_count/has_events/status. The WHERE clause is appended dynamically.
const sqliteReadRootsBase = `
SELECT
    cr.id,
    cr.timestamp,
    cr.service,
    cr.method,
    cr.path,
    cr.correlation_id,
    cr.source_ip,
    COUNT(e.id)                              AS event_count,
    CASE WHEN COUNT(e.id) > 0 THEN 1 ELSE 0 END AS has_events,
    MAX(CASE WHEN e.type = 'response' THEN e.status ELSE NULL END) AS status
FROM captured_requests cr
LEFT JOIN events e ON e.correlation_id = cr.correlation_id
`

const sqliteReadRootsGroupOrder = `
GROUP BY cr.id
ORDER BY cr.timestamp DESC, cr.id DESC
LIMIT ?
`

const sqliteReadRootsCursorWhere = `
WHERE (cr.timestamp < ? OR (cr.timestamp = ? AND cr.id < ?))
`

// ReadRoots returns captured-request rows from SQLite, sorted timestamp DESC,
// id DESC, paginated via cursor, limited to limit+1 rows. The extra row
// determines nextCursor.
func (s *SQLiteSink) ReadRoots(ctx context.Context, _ inspect.InspectQuery, limit int, cursor *inspect.Cursor) ([]inspect.RootRow, *inspect.Cursor, error) {
	var (
		query string
		args  []any
	)
	fetch := limit + 1
	if cursor != nil {
		nanos := cursor.Timestamp.UnixNano()
		query = sqliteReadRootsBase + sqliteReadRootsCursorWhere + sqliteReadRootsGroupOrder
		args = []any{nanos, nanos, cursor.ID, fetch}
	} else {
		query = sqliteReadRootsBase + sqliteReadRootsGroupOrder
		args = []any{fetch}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite ReadRoots: %w", err)
	}
	defer rows.Close()

	var result []inspect.RootRow
	for rows.Next() {
		var (
			id, service, method, path, corrID, sourceIP string
			tsNanos                                     int64
			eventCount                                  int
			hasEventsInt                                int
			statusVal                                   sql.NullInt64
		)
		if err := rows.Scan(&id, &tsNanos, &service, &method, &path, &corrID, &sourceIP,
			&eventCount, &hasEventsInt, &statusVal); err != nil {
			return nil, nil, fmt.Errorf("sqlite ReadRoots scan: %w", err)
		}
		row := inspect.RootRow{
			ID:            id,
			Kind:          "request",
			Timestamp:     time.Unix(0, tsNanos).UTC(),
			Service:       service,
			Method:        method,
			Path:          path,
			CorrelationID: corrID,
			SourceIP:      sourceIP,
			EventCount:    eventCount,
			HasEvents:     hasEventsInt != 0,
		}
		if statusVal.Valid {
			v := int(statusVal.Int64)
			row.Status = &v
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("sqlite ReadRoots rows: %w", err)
	}

	var nextCursor *inspect.Cursor
	if len(result) > limit {
		last := result[limit-1]
		nextCursor = &inspect.Cursor{
			Timestamp: last.Timestamp,
			ID:        last.ID,
		}
		result = result[:limit]
	}
	return result, nextCursor, nil
}

// ReadDetail returns ErrNotImplemented until the detail handler slice ships.
func (s *SQLiteSink) ReadDetail(_ context.Context, _ string) (inspect.DetailRecord, error) {
	return nil, inspect.ErrNotImplemented
}

// ServicesSeen returns distinct services from captured_requests written at or
// after since (zero means all time), ordered alphabetically.
func (s *SQLiteSink) ServicesSeen(ctx context.Context, since time.Time) ([]string, error) {
	var (
		query string
		args  []any
	)
	if since.IsZero() {
		query = `SELECT DISTINCT service FROM captured_requests ORDER BY service ASC`
	} else {
		query = `SELECT DISTINCT service FROM captured_requests WHERE timestamp >= ? ORDER BY service ASC`
		args = []any{since.UnixNano()}
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite ServicesSeen: %w", err)
	}
	defer rows.Close()

	var svcs []string
	for rows.Next() {
		var svc string
		if err := rows.Scan(&svc); err != nil {
			return nil, fmt.Errorf("sqlite ServicesSeen scan: %w", err)
		}
		svcs = append(svcs, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite ServicesSeen rows: %w", err)
	}
	return svcs, nil
}
