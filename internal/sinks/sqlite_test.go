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

func sampleRecord(id string) *capture.CapturedRequest {
	return &capture.CapturedRequest{
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

	// temp_store=MEMORY keeps sort/group/distinct spill in heap so the runtime
	// has no dependency on a writable temp directory (containers with
	// readOnlyRootFilesystem).
	var tempStore int
	if err := s.DB().QueryRow("PRAGMA temp_store").Scan(&tempStore); err != nil {
		t.Fatalf("PRAGMA temp_store: %v", err)
	}
	// 2 == MEMORY per https://www.sqlite.org/pragma.html#pragma_temp_store
	if tempStore != 2 {
		t.Errorf("temp_store: got %d want 2 (MEMORY)", tempStore)
	}

	// cache_size is set in KB (negative). -64000 == 64 MiB page cache.
	var cacheSize int
	if err := s.DB().QueryRow("PRAGMA cache_size").Scan(&cacheSize); err != nil {
		t.Fatalf("PRAGMA cache_size: %v", err)
	}
	if cacheSize != -64000 {
		t.Errorf("cache_size: got %d want -64000", cacheSize)
	}

	// mmap_size is per-connection. 268435456 == 256 MiB.
	var mmapSize int64
	if err := s.DB().QueryRow("PRAGMA mmap_size").Scan(&mmapSize); err != nil {
		t.Fatalf("PRAGMA mmap_size: %v", err)
	}
	if mmapSize != 268435456 {
		t.Errorf("mmap_size: got %d want 268435456", mmapSize)
	}

	wantIndexes := []string{
		"idx_captured_requests_timestamp",
		"idx_captured_requests_service",
		"idx_captured_requests_host",
		"idx_captured_requests_correlation_id",
		"idx_captured_requests_method",
		"idx_captured_requests_path",
		"idx_captured_requests_source_ip",
		"idx_events_timestamp",
		"idx_events_service",
		"idx_events_correlation_id",
		"idx_events_type",
		"idx_events_status",
		"idx_events_request_path",
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

func TestSQLiteSink_EventsTableSchema(t *testing.T) {
	t.Parallel()

	_, path := openTestSink(t)
	r := openReader(t, path)

	rows, err := r.Query("PRAGMA table_info(events)")
	if err != nil {
		t.Fatalf("PRAGMA table_info(events): %v", err)
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
	wantCols := []string{
		"id", "timestamp", "type", "correlation_id", "service", "service_source",
		"status", "duration_ms", "request_method", "request_path", "request_headers",
		"request_body", "request_body_truncated", "request_body_original_size",
		"response_status", "response_headers", "response_body",
		"response_body_truncated", "response_body_original_size",
	}
	for _, col := range wantCols {
		if _, ok := gotCols[col]; !ok {
			t.Errorf("missing events column %q", col)
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

func TestSQLiteSink_ResponseEventRoundTrip(t *testing.T) {
	t.Parallel()

	s, path := openTestSink(t)
	r := openReader(t, path)

	ts := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	evt := &capture.ResponseEvent{
		ID:                "resp-1",
		Timestamp:         ts,
		CorrelationID:     "corr-abc",
		CorrelationSource: capture.CorrelationSourceTraceparent,
		Service:           "users",
		ServiceSource:     capture.ServiceSourceHeader,
		Status:            201,
		Headers:           map[string][]string{"Content-Type": {"application/json"}},
		Body:              []byte(`{"created":true}`),
		BodyTruncated:     false,
		BodyOriginalSize:  16,
		ContentType:       "application/json",
		DurationMS:        55,
	}

	if err := s.Write(context.Background(), evt); err != nil {
		t.Fatalf("Write ResponseEvent: %v", err)
	}

	var (
		id, evtType, corrID, service string
		statusVal                    sql.NullInt64
		durationMS                   int64
		reqMethod, reqPath           sql.NullString
		reqHeaders, respHeaders      sql.NullString
		respBody                     []byte
	)
	err := r.QueryRow(`SELECT id, type, correlation_id, service, status, duration_ms,
		request_method, request_path, request_headers, response_headers, response_body
		FROM events WHERE id=?`, evt.ID,
	).Scan(&id, &evtType, &corrID, &service, &statusVal, &durationMS,
		&reqMethod, &reqPath, &reqHeaders, &respHeaders, &respBody)
	if err != nil {
		t.Fatalf("SELECT event: %v", err)
	}

	if evtType != "response" {
		t.Errorf("type: got %q want %q", evtType, "response")
	}
	if corrID != "corr-abc" {
		t.Errorf("correlation_id: got %q", corrID)
	}
	if !statusVal.Valid {
		t.Error("status: expected non-NULL for response event")
	} else if statusVal.Int64 != 201 {
		t.Errorf("status: got %d want 201", statusVal.Int64)
	}
	if reqMethod.Valid {
		t.Errorf("request_method: expected NULL for response event, got %q", reqMethod.String)
	}
	if reqPath.Valid {
		t.Errorf("request_path: expected NULL for response event, got %q", reqPath.String)
	}
	if reqHeaders.Valid {
		t.Errorf("request_headers: expected NULL for response event")
	}
	if !respHeaders.Valid {
		t.Error("response_headers: expected non-NULL for response event")
	}
	if string(respBody) != string(evt.Body) {
		t.Errorf("response_body: got %q want %q", respBody, evt.Body)
	}
	if durationMS != 55 {
		t.Errorf("duration_ms: got %d want 55", durationMS)
	}
	_ = id
}

func TestSQLiteSink_OutboundEventRoundTrip(t *testing.T) {
	t.Parallel()

	s, path := openTestSink(t)
	r := openReader(t, path)

	ts := time.Date(2026, 5, 18, 13, 0, 0, 0, time.UTC)
	evt := &capture.OutboundEvent{
		ID:                "out-1",
		Timestamp:         ts,
		CorrelationID:     "corr-xyz",
		CorrelationSource: capture.CorrelationSourceTraceparent,
		Service:           "orders",
		ServiceSource:     capture.ServiceSourceHeader,
		DurationMS:        38,
		Request: capture.OutboundRequestHalf{
			Method:           "POST",
			Path:             "/payments",
			Headers:          map[string][]string{"Content-Type": {"application/json"}},
			Body:             []byte(`{"amount":100}`),
			BodyTruncated:    false,
			BodyOriginalSize: 14,
			ContentType:      "application/json",
		},
		Response: &capture.OutboundResponseHalf{
			Status:           201,
			Headers:          map[string][]string{"X-Tx": {"abc"}},
			Body:             []byte(`{"ok":true}`),
			BodyTruncated:    false,
			BodyOriginalSize: 11,
			ContentType:      "application/json",
		},
	}

	if err := s.Write(context.Background(), evt); err != nil {
		t.Fatalf("Write OutboundEvent: %v", err)
	}

	var (
		id, evtType, corrID string
		topStatus           sql.NullInt64
		durationMS          int64
		reqMethod, reqPath  string
		reqHeaders          string
		respStatus          sql.NullInt64
		respHeaders         sql.NullString
		respBody            []byte
	)
	err := r.QueryRow(`SELECT id, type, correlation_id, status, duration_ms,
		request_method, request_path, request_headers,
		response_status, response_headers, response_body
		FROM events WHERE id=?`, evt.ID,
	).Scan(&id, &evtType, &corrID, &topStatus, &durationMS,
		&reqMethod, &reqPath, &reqHeaders,
		&respStatus, &respHeaders, &respBody)
	if err != nil {
		t.Fatalf("SELECT outbound event: %v", err)
	}

	if evtType != "outbound" {
		t.Errorf("type: got %q want %q", evtType, "outbound")
	}
	if topStatus.Valid {
		t.Errorf("top-level status: expected NULL for outbound event, got %d", topStatus.Int64)
	}
	if reqMethod != "POST" {
		t.Errorf("request_method: got %q want POST", reqMethod)
	}
	if reqPath != "/payments" {
		t.Errorf("request_path: got %q", reqPath)
	}
	if !respStatus.Valid {
		t.Error("response_status: expected non-NULL when response is present")
	} else if respStatus.Int64 != 201 {
		t.Errorf("response_status: got %d want 201", respStatus.Int64)
	}
	if string(respBody) != string(evt.Response.Body) {
		t.Errorf("response_body: got %q want %q", respBody, evt.Response.Body)
	}
	_ = durationMS
}

func TestSQLiteSink_OutboundEventNullResponse(t *testing.T) {
	t.Parallel()

	s, path := openTestSink(t)
	r := openReader(t, path)

	evt := &capture.OutboundEvent{
		ID:            "out-null",
		Timestamp:     time.Now().UTC(),
		CorrelationID: "corr-null",
		Service:       "orders",
		ServiceSource: capture.ServiceSourceHeader,
		DurationMS:    5,
		Request: capture.OutboundRequestHalf{
			Method: "GET",
			Path:   "/status",
		},
		Response: nil,
	}

	if err := s.Write(context.Background(), evt); err != nil {
		t.Fatalf("Write OutboundEvent (null response): %v", err)
	}

	var respStatus sql.NullInt64
	var respHeaders sql.NullString
	err := r.QueryRow(`SELECT response_status, response_headers FROM events WHERE id=?`, evt.ID,
	).Scan(&respStatus, &respHeaders)
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if respStatus.Valid {
		t.Errorf("response_status: expected NULL when response is nil, got %d", respStatus.Int64)
	}
	if respHeaders.Valid {
		t.Errorf("response_headers: expected NULL when response is nil, got %q", respHeaders.String)
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
