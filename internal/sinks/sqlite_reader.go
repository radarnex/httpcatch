package sinks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/inspect"
	"github.com/radarnex/httpcatch/internal/searchql"
)

// sqliteReadRootsBase is the captured-requests portion of the UNION query,
// joining against events for event_count/has_events/status. The WHERE and
// HAVING clauses are appended dynamically.
const sqliteReadRootsBase = `
SELECT
    cr.id,
    'request'                                AS kind,
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

// sqliteOrphansBase is the orphan-events portion of the UNION query: events
// whose correlation_id does not appear in captured_requests. The idx_events_correlation_id
// index is used for the LEFT JOIN probe; EXPLAIN QUERY PLAN confirms it.
//
// Orphan rows carry the event's own fields. Method, path, source_ip,
// event_count, and has_events are not applicable and are returned as NULL so
// the row shape matches the captured-request portion of the UNION.
const sqliteOrphansBase = `
SELECT
    e.id,
    CASE e.type WHEN 'response' THEN 'orphan_response' ELSE 'orphan_outbound' END AS kind,
    e.timestamp,
    e.service,
    NULL AS method,
    NULL AS path,
    e.correlation_id,
    NULL AS source_ip,
    NULL AS event_count,
    NULL AS has_events,
    CASE e.type WHEN 'response' THEN e.status ELSE NULL END AS status
FROM events e
LEFT JOIN captured_requests cr ON cr.correlation_id = e.correlation_id
WHERE cr.id IS NULL
`

// ReadRoots returns captured-request and orphan-event rows from SQLite,
// sorted timestamp DESC, id DESC, paginated via cursor, limited to limit+1
// rows. The extra row determines nextCursor. All filters in q are applied to
// the appropriate portion of the UNION.
//
// Filters that only apply to captured requests (method, path, source_ip,
// has_events=true) exclude orphan rows by definition. has_events=false matches
// captured requests with no correlated events — it does NOT mean orphan events.
// status and correlation_id/service/since/until are applied to orphan rows
// using the event's own fields.
func (s *SQLiteSink) ReadRoots(ctx context.Context, q inspect.InspectQuery, limit int, cursor *inspect.Cursor) ([]inspect.RootRow, *inspect.Cursor, error) {
	fetch := limit + 1

	// --- Captured-requests arm of the UNION ---

	var reqWhere []string
	var reqArgs []any

	if cursor != nil {
		nanos := cursor.Timestamp.UnixNano()
		reqWhere = append(reqWhere, "(cr.timestamp < ? OR (cr.timestamp = ? AND cr.id < ?))")
		reqArgs = append(reqArgs, nanos, nanos, cursor.ID)
	}
	if q.Since != nil {
		reqWhere = append(reqWhere, "cr.timestamp >= ?")
		reqArgs = append(reqArgs, q.Since.UnixNano())
	}
	if q.Until != nil {
		reqWhere = append(reqWhere, "cr.timestamp < ?")
		reqArgs = append(reqArgs, q.Until.UnixNano())
	}
	if termSQL, termArgs := searchql.CompileSQL(q.Query); termSQL != "" {
		reqWhere = append(reqWhere, termSQL)
		reqArgs = append(reqArgs, termArgs...)
	}

	var reqHaving []string
	var reqHavingArgs []any

	if havingSQL, havingArgs := searchql.CompileSQLHaving(q.Query); havingSQL != "" {
		reqHaving = append(reqHaving, havingSQL)
		reqHavingArgs = append(reqHavingArgs, havingArgs...)
	}

	reqQuery := sqliteReadRootsBase
	if len(reqWhere) > 0 {
		reqQuery += "\nWHERE " + strings.Join(reqWhere, "\n  AND ")
	}
	reqQuery += "\nGROUP BY cr.id"
	if len(reqHaving) > 0 {
		reqQuery += "\nHAVING " + strings.Join(reqHaving, "\n  AND ")
	}

	// --- Orphan-events arm of the UNION ---
	//
	// Request-only filters exclude orphan rows by definition: those fields
	// belong to captured requests only. When any is set, skip the orphan arm.
	includeOrphans := !q.HasRequestOnlyFilter()

	var orphanQuery string
	var orphanArgs []any

	if includeOrphans {
		var orphanWhere []string

		if cursor != nil {
			nanos := cursor.Timestamp.UnixNano()
			orphanWhere = append(orphanWhere, "(e.timestamp < ? OR (e.timestamp = ? AND e.id < ?))")
			orphanArgs = append(orphanArgs, nanos, nanos, cursor.ID)
		}
		if q.Since != nil {
			orphanWhere = append(orphanWhere, "e.timestamp >= ?")
			orphanArgs = append(orphanArgs, q.Since.UnixNano())
		}
		if q.Until != nil {
			orphanWhere = append(orphanWhere, "e.timestamp < ?")
			orphanArgs = append(orphanArgs, q.Until.UnixNano())
		}
		if orphanSQL, orphArgs := searchql.CompileSQLOrphans(q.Query); orphanSQL != "" {
			orphanWhere = append(orphanWhere, orphanSQL)
			orphanArgs = append(orphanArgs, orphArgs...)
		}

		orphanQuery = sqliteOrphansBase
		if len(orphanWhere) > 0 {
			// sqliteOrphansBase already has "WHERE cr.id IS NULL"; additional
			// filters are appended with AND.
			orphanQuery += "  AND " + strings.Join(orphanWhere, "\n  AND ")
		}
	}

	// --- Assemble UNION or single-arm query ---

	var fullQuery string
	var allArgs []any

	if includeOrphans {
		// Wrap the UNION in a subquery so the outer ORDER BY references the
		// unambiguous column names emitted by the UNION arms.
		fullQuery = "SELECT * FROM (\n" + reqQuery + "\nUNION ALL\n" + orphanQuery + "\n)\nORDER BY timestamp DESC, id DESC"
		// Args: req-where + req-having + orphan-where + LIMIT
		allArgs = append(allArgs, reqArgs...)
		allArgs = append(allArgs, reqHavingArgs...)
		allArgs = append(allArgs, orphanArgs...)
	} else {
		// Not a UNION: ORDER BY must qualify the table alias to avoid ambiguity
		// with the events table joined in sqliteReadRootsBase.
		fullQuery = reqQuery + "\nORDER BY cr.timestamp DESC, cr.id DESC"
		allArgs = append(allArgs, reqArgs...)
		allArgs = append(allArgs, reqHavingArgs...)
	}
	fullQuery += "\nLIMIT ?"
	allArgs = append(allArgs, fetch)

	rows, err := s.db.QueryContext(ctx, fullQuery, allArgs...)
	if err != nil {
		return nil, nil, fmt.Errorf("sqlite ReadRoots: %w", err)
	}
	defer rows.Close()

	var result []inspect.RootRow
	for rows.Next() {
		var (
			id, kind, corrID string
			service          string
			tsNanos          int64
			methodVal        sql.NullString
			pathVal          sql.NullString
			sourceIPVal      sql.NullString
			eventCountVal    sql.NullInt64
			hasEventsVal     sql.NullInt64
			statusVal        sql.NullInt64
		)
		if err := rows.Scan(&id, &kind, &tsNanos, &service,
			&methodVal, &pathVal, &corrID, &sourceIPVal,
			&eventCountVal, &hasEventsVal, &statusVal); err != nil {
			return nil, nil, fmt.Errorf("sqlite ReadRoots scan: %w", err)
		}
		row := inspect.RootRow{
			ID:            id,
			Kind:          kind,
			Timestamp:     time.Unix(0, tsNanos).UTC(),
			Service:       service,
			CorrelationID: corrID,
		}
		if methodVal.Valid {
			row.Method = methodVal.String
		}
		if pathVal.Valid {
			row.Path = pathVal.String
		}
		if sourceIPVal.Valid {
			row.SourceIP = sourceIPVal.String
		}
		if eventCountVal.Valid {
			v := int(eventCountVal.Int64)
			row.EventCount = &v
		}
		if hasEventsVal.Valid {
			v := hasEventsVal.Int64 != 0
			row.HasEvents = &v
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

// ReadDetail resolves the given id against both tables (captured_requests then
// events). Once the root record is identified, all other records sharing the
// same correlation_id are fetched as siblings, ordered by timestamp ascending.
// Returns ErrNotFound when the id does not appear in either table.
func (s *SQLiteSink) ReadDetail(ctx context.Context, id string) (inspect.DetailRecord, error) {
	root, corrID, err := s.sqliteFindRoot(ctx, id)
	if err != nil {
		return inspect.DetailRecord{}, err
	}

	siblings, err := s.sqliteFetchSiblings(ctx, id, corrID)
	if err != nil {
		return inspect.DetailRecord{}, err
	}

	return inspect.DetailRecord{Root: root, Events: siblings}, nil
}

// sqliteFindRoot looks up id in captured_requests first, then in events.
// Returns the record and its correlation_id, or ErrNotFound.
func (s *SQLiteSink) sqliteFindRoot(ctx context.Context, id string) (any, string, error) {
	cr, err := s.sqliteLoadRequest(ctx, id)
	if err != nil && !errors.Is(err, inspect.ErrNotFound) {
		return nil, "", err
	}
	if cr != nil {
		return cr, cr.CorrelationID, nil
	}

	ev, err := s.sqliteLoadEvent(ctx, id)
	if err != nil {
		return nil, "", err
	}
	return ev, ev.RecordCorrelationID(), nil
}

const sqliteSelectRequest = `
SELECT id, timestamp, service, service_source, host, correlation_id, correlation_source,
       method, path, source_ip, content_type, query, headers, cookies,
       body, body_truncated, body_original_size
FROM captured_requests WHERE id = ?`

// sqliteLoadRequest fetches a single CapturedRequest row by id.
// Returns (nil, ErrNotFound) when not found.
func (s *SQLiteSink) sqliteLoadRequest(ctx context.Context, id string) (*capture.CapturedRequest, error) {
	row := s.db.QueryRowContext(ctx, sqliteSelectRequest, id)
	cr, err := scanRequestRow(row.Scan)
	if err != nil {
		if errors.Is(err, inspect.ErrNotFound) {
			return nil, inspect.ErrNotFound
		}
		return nil, fmt.Errorf("sqlite load request %s: %w", id, err)
	}
	return cr, nil
}

// scanRequestRow reads one captured_requests-table row using the provided Scan
// function and returns a CapturedRequest. Returns ErrNotFound on sql.ErrNoRows.
func scanRequestRow(scan func(...any) error) (*capture.CapturedRequest, error) {
	var (
		rid, service, serviceSource, host, corrID, corrSource string
		method, path, sourceIP, contentType                   string
		tsNanos                                               int64
		queryJSON, headersJSON, cookiesJSON                   []byte
		body                                                  []byte
		bodyTruncatedInt, bodyOriginalSize                    int
	)
	err := scan(
		&rid, &tsNanos, &service, &serviceSource, &host, &corrID, &corrSource,
		&method, &path, &sourceIP, &contentType, &queryJSON, &headersJSON, &cookiesJSON,
		&body, &bodyTruncatedInt, &bodyOriginalSize,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, inspect.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan request row: %w", err)
	}

	var query map[string][]string
	if err := json.Unmarshal(queryJSON, &query); err != nil {
		return nil, fmt.Errorf("unmarshal query: %w", err)
	}
	var headers map[string][]string
	if err := json.Unmarshal(headersJSON, &headers); err != nil {
		return nil, fmt.Errorf("unmarshal headers: %w", err)
	}
	var cookies []capture.Cookie
	if err := json.Unmarshal(cookiesJSON, &cookies); err != nil {
		return nil, fmt.Errorf("unmarshal cookies: %w", err)
	}

	return &capture.CapturedRequest{
		ID:                rid,
		Timestamp:         time.Unix(0, tsNanos).UTC(),
		Service:           service,
		ServiceSource:     serviceSource,
		CorrelationID:     corrID,
		CorrelationSource: corrSource,
		Method:            method,
		Path:              path,
		SourceIP:          sourceIP,
		ContentType:       contentType,
		Query:             query,
		Headers:           headers,
		Cookies:           cookies,
		Body:              body,
		BodyTruncated:     bodyTruncatedInt != 0,
		BodyOriginalSize:  bodyOriginalSize,
	}, nil
}

const sqliteSelectEventByID = `
SELECT id, timestamp, type, correlation_id, service, service_source,
       status, duration_ms,
       request_method, request_path, request_headers, request_body,
       request_body_truncated, request_body_original_size,
       response_status, response_headers, response_body,
       response_body_truncated, response_body_original_size
FROM events WHERE id = ?`

// sqliteLoadEvent fetches a single event row by id and returns a typed Record.
// Returns ErrNotFound when the id is absent.
func (s *SQLiteSink) sqliteLoadEvent(ctx context.Context, id string) (capture.Record, error) {
	row := s.db.QueryRowContext(ctx, sqliteSelectEventByID, id)
	return scanEventRow(row.Scan)
}

const sqliteSelectEventsByCorr = `
SELECT id, timestamp, type, correlation_id, service, service_source,
       status, duration_ms,
       request_method, request_path, request_headers, request_body,
       request_body_truncated, request_body_original_size,
       response_status, response_headers, response_body,
       response_body_truncated, response_body_original_size
FROM events WHERE correlation_id = ? AND id != ?
ORDER BY timestamp ASC, id ASC`

const sqliteSelectRequestByCorr = `
SELECT id, timestamp, service, service_source, host, correlation_id, correlation_source,
       method, path, source_ip, content_type, query, headers, cookies,
       body, body_truncated, body_original_size
FROM captured_requests WHERE correlation_id = ? AND id != ?
ORDER BY timestamp ASC, id ASC`

// sqliteFetchSiblings returns all records (from both tables) that share corrID
// but whose id is not the root id, ordered by timestamp ascending.
func (s *SQLiteSink) sqliteFetchSiblings(ctx context.Context, rootID, corrID string) ([]any, error) {
	// Fetch sibling captured_requests.
	reqRows, err := s.db.QueryContext(ctx, sqliteSelectRequestByCorr, corrID, rootID)
	if err != nil {
		return nil, fmt.Errorf("sqlite sibling requests: %w", err)
	}
	defer reqRows.Close()

	all := []any{}
	for reqRows.Next() {
		cr, err := scanRequestRow(reqRows.Scan)
		if err != nil {
			return nil, fmt.Errorf("sqlite sibling request scan: %w", err)
		}
		all = append(all, cr)
	}
	if err := reqRows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite sibling requests rows: %w", err)
	}

	// Fetch sibling events.
	evtRows, err := s.db.QueryContext(ctx, sqliteSelectEventsByCorr, corrID, rootID)
	if err != nil {
		return nil, fmt.Errorf("sqlite sibling events: %w", err)
	}
	defer evtRows.Close()

	for evtRows.Next() {
		rec, err := scanEventRow(evtRows.Scan)
		if err != nil {
			return nil, fmt.Errorf("sqlite sibling event scan: %w", err)
		}
		all = append(all, rec)
	}
	if err := evtRows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite sibling events rows: %w", err)
	}

	// Sort merged result by (timestamp ASC, id ASC).
	sort.SliceStable(all, func(i, j int) bool {
		ri := all[i].(capture.Record)
		rj := all[j].(capture.Record)
		ti := ri.RecordTimestamp()
		tj := rj.RecordTimestamp()
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return ri.RecordID() < rj.RecordID()
	})
	return all, nil
}

// scanEventRow reads one events-table row using the provided Scan function and
// returns the appropriate capture.Record variant.
func scanEventRow(scan func(...any) error) (capture.Record, error) {
	var (
		id, evtType, corrID, service, serviceSource string
		tsNanos, durationMS                         int64
		statusVal                                   sql.NullInt64
		reqMethod, reqPath                          sql.NullString
		reqHeadersJSON, reqBody                     []byte
		reqBodyTruncatedInt                         sql.NullInt64
		reqBodyOriginalSize                         sql.NullInt64
		respStatus                                  sql.NullInt64
		respHeadersJSON, respBody                   []byte
		respBodyTruncatedInt                        sql.NullInt64
		respBodyOriginalSize                        sql.NullInt64
	)
	err := scan(
		&id, &tsNanos, &evtType, &corrID, &service, &serviceSource,
		&statusVal, &durationMS,
		&reqMethod, &reqPath, &reqHeadersJSON, &reqBody,
		&reqBodyTruncatedInt, &reqBodyOriginalSize,
		&respStatus, &respHeadersJSON, &respBody,
		&respBodyTruncatedInt, &respBodyOriginalSize,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, inspect.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan event row: %w", err)
	}

	ts := time.Unix(0, tsNanos).UTC()

	switch evtType {
	case "response":
		var headers map[string][]string
		if len(respHeadersJSON) > 0 {
			if err := json.Unmarshal(respHeadersJSON, &headers); err != nil {
				return nil, fmt.Errorf("unmarshal response event headers: %w", err)
			}
		}
		status := 0
		if statusVal.Valid {
			status = int(statusVal.Int64)
		}
		truncated := respBodyTruncatedInt.Valid && respBodyTruncatedInt.Int64 != 0
		origSize := 0
		if respBodyOriginalSize.Valid {
			origSize = int(respBodyOriginalSize.Int64)
		}
		return &capture.ResponseEvent{
			ID:                id,
			Timestamp:         ts,
			CorrelationID:     corrID,
			Service:           service,
			ServiceSource:     serviceSource,
			Status:            status,
			Headers:           headers,
			Body:              respBody,
			BodyTruncated:     truncated,
			BodyOriginalSize:  origSize,
			DurationMS:        durationMS,
		}, nil

	case "outbound":
		var reqHeaders map[string][]string
		if len(reqHeadersJSON) > 0 {
			if err := json.Unmarshal(reqHeadersJSON, &reqHeaders); err != nil {
				return nil, fmt.Errorf("unmarshal outbound request headers: %w", err)
			}
		}
		reqTruncated := reqBodyTruncatedInt.Valid && reqBodyTruncatedInt.Int64 != 0
		reqOrigSize := 0
		if reqBodyOriginalSize.Valid {
			reqOrigSize = int(reqBodyOriginalSize.Int64)
		}

		var resp *capture.OutboundResponseHalf
		if respStatus.Valid {
			var respHeaders map[string][]string
			if len(respHeadersJSON) > 0 {
				if err := json.Unmarshal(respHeadersJSON, &respHeaders); err != nil {
					return nil, fmt.Errorf("unmarshal outbound response headers: %w", err)
				}
			}
			respTruncated := respBodyTruncatedInt.Valid && respBodyTruncatedInt.Int64 != 0
			respOrigSize := 0
			if respBodyOriginalSize.Valid {
				respOrigSize = int(respBodyOriginalSize.Int64)
			}
			resp = &capture.OutboundResponseHalf{
				Status:           int(respStatus.Int64),
				Headers:          respHeaders,
				Body:             respBody,
				BodyTruncated:    respTruncated,
				BodyOriginalSize: respOrigSize,
			}
		}

		return &capture.OutboundEvent{
			ID:            id,
			Timestamp:     ts,
			CorrelationID: corrID,
			Service:       service,
			ServiceSource: serviceSource,
			DurationMS:    durationMS,
			Request: capture.OutboundRequestHalf{
				Method:           reqMethod.String,
				Path:             reqPath.String,
				Headers:          reqHeaders,
				Body:             reqBody,
				BodyTruncated:    reqTruncated,
				BodyOriginalSize: reqOrigSize,
			},
			Response: resp,
		}, nil

	default:
		return nil, fmt.Errorf("unknown event type %q for id %s", evtType, id)
	}
}

// OrphanCounts returns the count of orphan response events and orphan outbound
// events in the events table — events whose correlation_id does not appear in
// captured_requests. Computed at call time via the LEFT JOIN; this is a gauge
// sampled on each /metrics scrape.
func (s *SQLiteSink) OrphanCounts(ctx context.Context) (response, outbound int, err error) {
	const q = `
SELECT
    SUM(CASE WHEN e.type = 'response' THEN 1 ELSE 0 END),
    SUM(CASE WHEN e.type = 'outbound' THEN 1 ELSE 0 END)
FROM events e
LEFT JOIN captured_requests cr ON cr.correlation_id = e.correlation_id
WHERE cr.id IS NULL`
	row := s.db.QueryRowContext(ctx, q)
	var respVal, outboundVal sql.NullInt64
	if err := row.Scan(&respVal, &outboundVal); err != nil {
		return 0, 0, fmt.Errorf("sqlite OrphanCounts: %w", err)
	}
	if respVal.Valid {
		response = int(respVal.Int64)
	}
	if outboundVal.Valid {
		outbound = int(outboundVal.Int64)
	}
	return response, outbound, nil
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
