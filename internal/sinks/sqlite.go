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
`

const sqlitePragmas = `
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
`

const sqliteInsert = `INSERT INTO captured_requests (
    id, timestamp, service, service_source, host, correlation_id, correlation_source,
    method, path, source_ip, content_type, query, headers, cookies,
    body, body_truncated, body_original_size
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

// SQLiteSink persists captured records to a SQLite file using the pure-Go
// modernc.org/sqlite driver so the project builds with CGO_ENABLED=0.
type SQLiteSink struct {
	db   *sql.DB
	stmt *sql.Stmt
}

// NewSQLiteSink opens (or creates) the SQLite database at path, applies WAL
// and synchronous=NORMAL pragmas, ensures the schema, and prepares the
// insert statement. The configured directory must already exist and be
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
	stmt, err := db.Prepare(sqliteInsert)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("prepare sqlite insert: %w", err)
	}
	return &SQLiteSink{db: db, stmt: stmt}, nil
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
	stmtErr := s.stmt.Close()
	dbErr := s.db.Close()
	if stmtErr != nil {
		return stmtErr
	}
	return dbErr
}

func (s *SQLiteSink) Write(ctx context.Context, r *capture.CapturedRecord) error {
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

	if _, err := s.stmt.ExecContext(ctx,
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
		return fmt.Errorf("insert record %s: %w", r.ID, err)
	}
	return nil
}
