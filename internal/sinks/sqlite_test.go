package sinks

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/radarnex/httpcatch/internal/capture"
)

func sampleRecord(id string) *capture.CapturedRecord {
	return &capture.CapturedRecord{
		ID:        id,
		Timestamp: time.Date(2026, 5, 18, 12, 34, 56, 789, time.UTC),
		Method:    "POST",
		Path:      "/api/v1/orders",
		Query:     map[string][]string{"page": {"3"}, "limit": {"100"}},
		Headers: map[string][]string{
			"Content-Type":        {"application/json"},
			"Host":                {"orders.example.com"},
			"X-Httpcatch-Service": {"orders"},
			"X-Request-Id":        {"req-" + id},
			"Accept":              {"application/json"},
		},
		Cookies:           []capture.Cookie{{Name: "sid", Value: "abc"}},
		Body:              []byte(`{"hello":"world"}`),
		BodyTruncated:     false,
		BodyOriginalSize:  17,
		ContentType:       "application/json",
		SourceIP:          "10.0.0.42",
		Service:           "orders",
		ServiceSource:     capture.ServiceSourceHeader,
		CorrelationID:     "0af7651916cd43dd8448eb211c80319c",
		CorrelationSource: capture.CorrelationSourceTraceparent,
	}
}

func openTestSink(t *testing.T) (*SQLiteSink, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	s, err := NewSQLiteSink(path)
	if err != nil {
		t.Fatalf("NewSQLiteSink: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

// openReader opens a separate handle so tests inspect the same file the sink
// writes to, without going through the sink's prepared-statement connection.
func openReader(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestSQLiteSink_Name(t *testing.T) {
	t.Parallel()

	s, _ := openTestSink(t)
	if s.Name() != NameSQLite {
		t.Errorf("Name: got %q want %q", s.Name(), NameSQLite)
	}
}

func TestSQLiteSink_SchemaPragmasAndIndexes(t *testing.T) {
	t.Parallel()

	s, path := openTestSink(t)
	r := openReader(t, path)

	var journal string
	if err := r.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if journal != "wal" {
		t.Errorf("journal_mode: got %q want %q", journal, "wal")
	}

	// synchronous is per-connection — verify through the sink's pinned pool.
	var sync int
	if err := s.DB().QueryRow("PRAGMA synchronous").Scan(&sync); err != nil {
		t.Fatalf("PRAGMA synchronous: %v", err)
	}
	// 1 == NORMAL per https://www.sqlite.org/pragma.html#pragma_synchronous
	if sync != 1 {
		t.Errorf("synchronous: got %d want 1 (NORMAL)", sync)
	}

	wantIndexes := []string{
		"idx_captured_requests_timestamp",
		"idx_captured_requests_service",
		"idx_captured_requests_host",
		"idx_captured_requests_correlation_id",
		"idx_captured_requests_method",
		"idx_captured_requests_path",
		"idx_captured_requests_source_ip",
	}
	for _, idx := range wantIndexes {
		var name string
		err := r.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", idx,
		).Scan(&name)
		if err != nil {
			t.Errorf("missing index %q: %v", idx, err)
		}
	}

	rows, err := r.Query("PRAGMA table_info(captured_requests)")
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()
	gotCols := map[string]string{}
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		gotCols[name] = typ
	}
	wantCols := map[string]string{
		"id":                 "TEXT",
		"timestamp":          "INTEGER",
		"service":            "TEXT",
		"service_source":     "TEXT",
		"host":               "TEXT",
		"correlation_id":     "TEXT",
		"correlation_source": "TEXT",
		"method":             "TEXT",
		"path":               "TEXT",
		"source_ip":          "TEXT",
		"content_type":       "TEXT",
		"query":              "TEXT",
		"headers":            "TEXT",
		"cookies":            "TEXT",
		"body":               "BLOB",
		"body_truncated":     "INTEGER",
		"body_original_size": "INTEGER",
	}
	for col, typ := range wantCols {
		if got, ok := gotCols[col]; !ok {
			t.Errorf("missing column %q", col)
		} else if got != typ {
			t.Errorf("column %q type: got %q want %q", col, got, typ)
		}
	}
}

func TestSQLiteSink_RoundTrip(t *testing.T) {
	t.Parallel()

	s, path := openTestSink(t)
	r := openReader(t, path)
	rec := sampleRecord("rt1")
	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var (
		id, service, serviceSource, host, corrID, corrSource string
		method, path2, sourceIP, contentType                 string
		queryJSON, headersJSON, cookiesJSON                  string
		body                                                 []byte
		truncated, origSize                                  int
		tsNanos                                              int64
	)
	err := r.QueryRow(`SELECT id, timestamp, service, service_source, host,
        correlation_id, correlation_source, method, path, source_ip,
        content_type, query, headers, cookies, body, body_truncated,
        body_original_size FROM captured_requests WHERE id=?`, rec.ID,
	).Scan(&id, &tsNanos, &service, &serviceSource, &host, &corrID,
		&corrSource, &method, &path2, &sourceIP, &contentType,
		&queryJSON, &headersJSON, &cookiesJSON, &body, &truncated, &origSize)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}

	if id != rec.ID {
		t.Errorf("id: got %q want %q", id, rec.ID)
	}
	if tsNanos != rec.Timestamp.UnixNano() {
		t.Errorf("timestamp: got %d want %d", tsNanos, rec.Timestamp.UnixNano())
	}
	if service != rec.Service {
		t.Errorf("service: got %q want %q", service, rec.Service)
	}
	if serviceSource != rec.ServiceSource {
		t.Errorf("service_source: got %q want %q", serviceSource, rec.ServiceSource)
	}
	if host != http.Header(rec.Headers).Get(capture.HostHeader) {
		t.Errorf("host: got %q want %q", host, http.Header(rec.Headers).Get(capture.HostHeader))
	}
	if corrID != rec.CorrelationID {
		t.Errorf("correlation_id: got %q want %q", corrID, rec.CorrelationID)
	}
	if corrSource != rec.CorrelationSource {
		t.Errorf("correlation_source: got %q want %q", corrSource, rec.CorrelationSource)
	}
	if method != rec.Method {
		t.Errorf("method: got %q want %q", method, rec.Method)
	}
	if path2 != rec.Path {
		t.Errorf("path: got %q want %q", path2, rec.Path)
	}
	if sourceIP != rec.SourceIP {
		t.Errorf("source_ip: got %q want %q", sourceIP, rec.SourceIP)
	}
	if contentType != rec.ContentType {
		t.Errorf("content_type: got %q want %q", contentType, rec.ContentType)
	}
	if origSize != rec.BodyOriginalSize {
		t.Errorf("body_original_size: got %d want %d", origSize, rec.BodyOriginalSize)
	}
	if (truncated == 1) != rec.BodyTruncated {
		t.Errorf("body_truncated: got %d want %v", truncated, rec.BodyTruncated)
	}
	if string(body) != string(rec.Body) {
		t.Errorf("body: got %q want %q", body, rec.Body)
	}

	var gotQuery map[string][]string
	if err := json.Unmarshal([]byte(queryJSON), &gotQuery); err != nil {
		t.Fatalf("query JSON: %v", err)
	}
	if gotQuery["page"][0] != "3" || gotQuery["limit"][0] != "100" {
		t.Errorf("query roundtrip mismatch: %v", gotQuery)
	}

	var gotHeaders map[string][]string
	if err := json.Unmarshal([]byte(headersJSON), &gotHeaders); err != nil {
		t.Fatalf("headers JSON: %v", err)
	}
	if gotHeaders["X-Httpcatch-Service"][0] != "orders" {
		t.Errorf("headers roundtrip missing service header: %v", gotHeaders)
	}

	var gotCookies []capture.Cookie
	if err := json.Unmarshal([]byte(cookiesJSON), &gotCookies); err != nil {
		t.Fatalf("cookies JSON: %v", err)
	}
	if len(gotCookies) != 1 || gotCookies[0].Name != "sid" || gotCookies[0].Value != "abc" {
		t.Errorf("cookies roundtrip mismatch: %v", gotCookies)
	}
}

func TestSQLiteSink_TruncatedBody(t *testing.T) {
	t.Parallel()

	s, path := openTestSink(t)
	r := openReader(t, path)

	rec := sampleRecord("trunc1")
	rec.Body = []byte("xxxxxxxxxx") // 10 bytes after cap
	rec.BodyTruncated = true
	rec.BodyOriginalSize = 4096

	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatalf("Write: %v", err)
	}
	var (
		truncated, origSize int
		body                []byte
	)
	err := r.QueryRow(
		"SELECT body_truncated, body_original_size, body FROM captured_requests WHERE id=?",
		rec.ID,
	).Scan(&truncated, &origSize, &body)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if truncated != 1 {
		t.Errorf("body_truncated: got %d want 1", truncated)
	}
	if origSize != 4096 {
		t.Errorf("body_original_size: got %d want 4096", origSize)
	}
	if len(body) != 10 {
		t.Errorf("body length: got %d want 10 (already-capped bytes)", len(body))
	}
}

func TestSQLiteSink_ReopenReusesSchema(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "reopen.db")
	s, err := NewSQLiteSink(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	rec := sampleRecord("r1")
	if err := s.Write(context.Background(), rec); err != nil {
		t.Fatalf("Write 1: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := NewSQLiteSink(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	rec2 := sampleRecord("r2")
	if err := s2.Write(context.Background(), rec2); err != nil {
		t.Fatalf("Write 2: %v", err)
	}

	r := openReader(t, path)
	var count int
	if err := r.QueryRow("SELECT COUNT(*) FROM captured_requests").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("row count after reopen: got %d want 2", count)
	}
}

func TestSQLiteSink_ConcurrentWrites(t *testing.T) {
	t.Parallel()

	s, path := openTestSink(t)
	const (
		writers   = 8
		perWriter = 50
	)
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range perWriter {
				rec := sampleRecord(fmt.Sprintf("w%d-%d", w, i))
				if err := s.Write(context.Background(), rec); err != nil {
					t.Errorf("Write w=%d i=%d: %v", w, i, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	r := openReader(t, path)
	var count int
	if err := r.QueryRow("SELECT COUNT(*) FROM captured_requests").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	want := writers * perWriter
	if count != want {
		t.Errorf("row count: got %d want %d", count, want)
	}
}

func TestSQLiteSink_RejectsMissingDirectory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	missing := filepath.Join(dir, "nope", "x.db")
	_, err := NewSQLiteSink(missing)
	if err == nil {
		t.Fatal("expected error for missing parent directory")
	}
}

func TestSQLiteSink_RejectsUnwritableDirectory(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		t.Skip("posix-mode directory permission test")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	_, err := NewSQLiteSink(filepath.Join(locked, "x.db"))
	if err == nil {
		t.Fatal("expected error for unwritable parent directory")
	}
}

func TestSQLiteSink_RejectsEmptyPath(t *testing.T) {
	t.Parallel()

	_, err := NewSQLiteSink("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestSQLiteSink_OrderingByTimestamp(t *testing.T) {
	t.Parallel()

	s, path := openTestSink(t)
	now := time.Now().UTC()
	insertOrder := []string{"c", "a", "b"}
	for i, id := range insertOrder {
		rec := sampleRecord(id)
		rec.Timestamp = now.Add(time.Duration(i) * time.Millisecond)
		if err := s.Write(context.Background(), rec); err != nil {
			t.Fatalf("Write %s: %v", id, err)
		}
	}
	r := openReader(t, path)
	rows, err := r.Query("SELECT id FROM captured_requests ORDER BY timestamp ASC")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if len(got) != len(insertOrder) {
		t.Fatalf("got %v want %v", got, insertOrder)
	}
	for i := range got {
		if got[i] != insertOrder[i] {
			t.Errorf("[%d]: got %q want %q", i, got[i], insertOrder[i])
		}
	}
}
