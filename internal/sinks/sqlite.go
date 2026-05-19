package sinks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/radarnex/httpcatch/internal/capture"
)

const (
	NameSQLite         = "sqlite"
	sqliteBusyTimeout  = 5000
	sqliteMaxOpenConns = 1
)

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS captured_requests (
    id TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,
    service TEXT NOT NULL,
    service_source TEXT NOT NULL,
    host TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    correlation_source TEXT NOT NULL,
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    source_ip TEXT NOT NULL,
    content_type TEXT NOT NULL,
    query TEXT NOT NULL,
    headers TEXT NOT NULL,
    cookies TEXT NOT NULL,
    body BLOB NOT NULL,
    body_truncated INTEGER NOT NULL,
    body_original_size INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_captured_requests_timestamp ON captured_requests(timestamp);
CREATE INDEX IF NOT EXISTS idx_captured_requests_service ON captured_requests(service);
CREATE INDEX IF NOT EXISTS idx_captured_requests_host ON captured_requests(host);
CREATE INDEX IF NOT EXISTS idx_captured_requests_correlation_id ON captured_requests(correlation_id);
CREATE INDEX IF NOT EXISTS idx_captured_requests_method ON captured_requests(method);
CREATE INDEX IF NOT EXISTS idx_captured_requests_path ON captured_requests(path);
CREATE INDEX IF NOT EXISTS idx_captured_requests_source_ip ON captured_requests(source_ip);

CREATE TABLE IF NOT EXISTS events (
    id TEXT PRIMARY KEY,
    timestamp INTEGER NOT NULL,
    type TEXT NOT NULL,
    correlation_id TEXT NOT NULL,
    service TEXT NOT NULL,
    service_source TEXT NOT NULL,
    status INTEGER,
    duration_ms INTEGER NOT NULL,
    request_method TEXT,
    request_path TEXT,
    request_headers TEXT,
    request_body BLOB,
    request_body_truncated INTEGER,
    request_body_original_size INTEGER,
    response_status INTEGER,
    response_headers TEXT,
    response_body BLOB,
    response_body_truncated INTEGER,
    response_body_original_size INTEGER
);
CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);
CREATE INDEX IF NOT EXISTS idx_events_service ON events(service);
CREATE INDEX IF NOT EXISTS idx_events_correlation_id ON events(correlation_id);
CREATE INDEX IF NOT EXISTS idx_events_type ON events(type);
CREATE INDEX IF NOT EXISTS idx_events_status ON events(status);
`

const sqlitePragmas = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
`

const sqliteInsertRequest = `INSERT INTO captured_requests (
    id, timestamp, service, service_source, host, correlation_id, correlation_source,
    method, path, source_ip, content_type, query, headers, cookies,
    body, body_truncated, body_original_size
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

const sqliteInsertEvent = `INSERT INTO events (
    id, timestamp, type, correlation_id, service, service_source,
    status, duration_ms,
    request_method, request_path, request_headers, request_body,
    request_body_truncated, request_body_original_size,
    response_status, response_headers, response_body,
    response_body_truncated, response_body_original_size
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// SQLiteSink persists captured records to a SQLite file using the pure-Go
// modernc.org/sqlite driver so the project builds with CGO_ENABLED=0.
type SQLiteSink struct {
	db          *sql.DB
	stmtRequest *sql.Stmt
	stmtEvent   *sql.Stmt
}

// NewSQLiteSink opens (or creates) the SQLite database at path, applies WAL
// and synchronous=NORMAL pragmas, ensures the schema, and prepares the
// insert statements. The configured directory must already exist and be
// writable; startup fails otherwise.
func NewSQLiteSink(path string) (*SQLiteSink, error) {
	if path == "" {
		return nil, fmt.Errorf("sqlite path is empty")
	}
	if err := checkSQLiteDir(path); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(sqliteMaxOpenConns)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", path, err)
	}
	if _, err := db.Exec(sqlitePragmas); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply sqlite pragmas in %q: %w", path, err)
	}
	if _, err := db.Exec(sqliteSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init sqlite schema in %q: %w", path, err)
	}
	stmtRequest, err := db.Prepare(sqliteInsertRequest)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("prepare sqlite request insert: %w", err)
	}
	stmtEvent, err := db.Prepare(sqliteInsertEvent)
	if err != nil {
		stmtRequest.Close()
		db.Close()
		return nil, fmt.Errorf("prepare sqlite event insert: %w", err)
	}
	return &SQLiteSink{db: db, stmtRequest: stmtRequest, stmtEvent: stmtEvent}, nil
}

// checkSQLiteDir surfaces a clear startup error for a missing or unwritable
// parent directory, instead of deferring it to the first record write.
func checkSQLiteDir(path string) error {
	dir := filepath.Dir(path)
	probe, err := os.CreateTemp(dir, ".httpcatch-write-check-*")
	if err != nil {
		return fmt.Errorf("sqlite directory %q is not usable: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

func (s *SQLiteSink) Name() string { return NameSQLite }

// DB returns the underlying database handle. Tests use it to read
// per-connection PRAGMAs through the same pool the sink writes through.
func (s *SQLiteSink) DB() *sql.DB { return s.db }

func (s *SQLiteSink) Close() error {
	reqErr := s.stmtRequest.Close()
	evtErr := s.stmtEvent.Close()
	dbErr := s.db.Close()
	if reqErr != nil {
		return reqErr
	}
	if evtErr != nil {
		return evtErr
	}
	return dbErr
}

func (s *SQLiteSink) Write(ctx context.Context, r capture.Record) error {
	switch v := r.(type) {
	case *capture.CapturedRequest:
		return s.writeCapturedRequest(ctx, v)
	case *capture.ResponseEvent:
		return s.writeResponseEvent(ctx, v)
	case *capture.OutboundEvent:
		return s.writeOutboundEvent(ctx, v)
	default:
		return fmt.Errorf("sqlite sink: unknown record kind %T", r)
	}
}

func (s *SQLiteSink) writeCapturedRequest(ctx context.Context, r *capture.CapturedRequest) error {
	queryJSON, err := json.Marshal(r.Query)
	if err != nil {
		return fmt.Errorf("marshal query: %w", err)
	}
	headersJSON, err := json.Marshal(r.Headers)
	if err != nil {
		return fmt.Errorf("marshal headers: %w", err)
	}
	cookiesJSON, err := json.Marshal(r.Cookies)
	if err != nil {
		return fmt.Errorf("marshal cookies: %w", err)
	}

	// The capture handler canonicalises and sets Host before fan-out, so a
	// direct map lookup avoids http.Header.Get's per-call canonicalisation.
	var host string
	if vs := r.Headers[capture.HostHeader]; len(vs) > 0 {
		host = vs[0]
	}
	truncated := 0
	if r.BodyTruncated {
		truncated = 1
	}

	if _, err := s.stmtRequest.ExecContext(ctx,
		r.ID,
		r.Timestamp.UnixNano(),
		r.Service,
		r.ServiceSource,
		host,
		r.CorrelationID,
		r.CorrelationSource,
		r.Method,
		r.Path,
		r.SourceIP,
		r.ContentType,
		queryJSON,
		headersJSON,
		cookiesJSON,
		r.Body,
		truncated,
		r.BodyOriginalSize,
	); err != nil {
		return fmt.Errorf("insert request %s: %w", r.ID, err)
	}
	return nil
}

func (s *SQLiteSink) writeResponseEvent(ctx context.Context, r *capture.ResponseEvent) error {
	headersJSON, err := json.Marshal(r.Headers)
	if err != nil {
		return fmt.Errorf("marshal response event headers: %w", err)
	}
	truncated := 0
	if r.BodyTruncated {
		truncated = 1
	}

	if _, err := s.stmtEvent.ExecContext(ctx,
		r.ID,
		r.Timestamp.UnixNano(),
		"response",
		r.CorrelationID,
		r.Service,
		r.ServiceSource,
		r.Status,
		r.DurationMS,
		nil, // request_method — NULL for response events
		nil, // request_path
		nil, // request_headers
		nil, // request_body
		nil, // request_body_truncated
		nil, // request_body_original_size
		nil, // response_status — response events use top-level status
		headersJSON,
		r.Body,
		truncated,
		r.BodyOriginalSize,
	); err != nil {
		return fmt.Errorf("insert response event %s: %w", r.ID, err)
	}
	return nil
}

func (s *SQLiteSink) writeOutboundEvent(ctx context.Context, r *capture.OutboundEvent) error {
	reqHeadersJSON, err := json.Marshal(r.Request.Headers)
	if err != nil {
		return fmt.Errorf("marshal outbound request headers: %w", err)
	}
	reqTruncated := 0
	if r.Request.BodyTruncated {
		reqTruncated = 1
	}

	var (
		respStatus           any
		respHeadersJSON      any
		respBody             any
		respBodyTruncated    any
		respBodyOriginalSize any
	)
	if r.Response != nil {
		h, err := json.Marshal(r.Response.Headers)
		if err != nil {
			return fmt.Errorf("marshal outbound response headers: %w", err)
		}
		rt := 0
		if r.Response.BodyTruncated {
			rt = 1
		}
		respStatus = r.Response.Status
		respHeadersJSON = h
		respBody = r.Response.Body
		respBodyTruncated = rt
		respBodyOriginalSize = r.Response.BodyOriginalSize
	}

	if _, err := s.stmtEvent.ExecContext(ctx,
		r.ID,
		r.Timestamp.UnixNano(),
		"outbound",
		r.CorrelationID,
		r.Service,
		r.ServiceSource,
		nil, // status — NULL at the top level for outbound; response half may carry one
		r.DurationMS,
		r.Request.Method,
		r.Request.Path,
		reqHeadersJSON,
		r.Request.Body,
		reqTruncated,
		r.Request.BodyOriginalSize,
		respStatus,
		respHeadersJSON,
		respBody,
		respBodyTruncated,
		respBodyOriginalSize,
	); err != nil {
		return fmt.Errorf("insert outbound event %s: %w", r.ID, err)
	}
	return nil
}
