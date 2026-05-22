package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/radarnex/httpcatch/internal/inspect"
)

const (
	readSourceMemory      = "memory"
	readSourceSQLite      = "sqlite"
	readSourceMemorySQLite = "memory+sqlite"
	readSourceNone        = "none"

	defaultLimit = 50
	maxLimit     = 500

	histogramBucketCount = 40
	// maxHistogramBuckets caps client-supplied `buckets` so a stray large
	// value cannot force an unbounded GROUP BY.
	maxHistogramBuckets = 240
)

type rootsResponse struct {
	Records    []inspect.RootRow `json:"records"`
	NextCursor *string           `json:"next_cursor"`
}

// gatherRoots fetches and merges root rows from whichever readers are enabled,
// applying the memory→sqlite fall-through and dedup logic. Returns the rows,
// the next cursor (nil when no further pages), the read source label, and any
// error. When both readers are nil the caller receives (nil, nil, readSourceNone, nil).
func gatherRoots(ctx context.Context, q inspect.InspectQuery, memReader, sqlReader inspect.Reader) ([]inspect.RootRow, *inspect.Cursor, string, error) {
	limit := q.Limit

	if memReader == nil && sqlReader == nil {
		return nil, nil, readSourceNone, nil
	}

	// Any non-temporal filter forces SQLite-only reads. Memory cannot apply
	// joins (e.g. status via the events table) and filtered queries must not
	// silently miss records that aged out of the ring buffer.
	if hasNonTemporalFilter(q) || memReader == nil {
		if sqlReader == nil {
			return nil, nil, readSourceNone, nil
		}
		rows, next, err := sqlReader.ReadRoots(ctx, q, limit, q.Cursor)
		if err != nil {
			return nil, nil, "", err
		}
		return rows, next, readSourceSQLite, nil
	}

	// Memory-eligible path: temporal-only or no filters.
	memRows, memNext, err := memReader.ReadRoots(ctx, q, limit, q.Cursor)
	if err != nil {
		return nil, nil, "", err
	}
	if len(memRows) >= limit || sqlReader == nil {
		return memRows, memNext, readSourceMemory, nil
	}

	// Memory yielded fewer rows than requested; fall through to SQLite for the
	// full page. Deduplicate by id across both sets, re-sort, then trim to limit.
	memIDs := make(map[string]struct{}, len(memRows))
	for _, row := range memRows {
		memIDs[row.ID] = struct{}{}
	}
	sqlRows, _, err := sqlReader.ReadRoots(ctx, q, limit, q.Cursor)
	if err != nil {
		return nil, nil, "", err
	}
	deduped := make([]inspect.RootRow, 0, len(memRows)+len(sqlRows))
	deduped = append(deduped, memRows...)
	for _, row := range sqlRows {
		if _, dup := memIDs[row.ID]; !dup {
			deduped = append(deduped, row)
		}
	}
	// Re-sort merged result timestamp DESC, id DESC.
	sort.SliceStable(deduped, func(i, j int) bool {
		ti := deduped[i].Timestamp
		tj := deduped[j].Timestamp
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return deduped[i].ID > deduped[j].ID
	})
	// Trim to limit and compute next cursor.
	var nextCur *inspect.Cursor
	if len(deduped) > limit {
		last := deduped[limit-1]
		nextCur = &inspect.Cursor{Timestamp: last.Timestamp, ID: last.ID}
		deduped = deduped[:limit]
	}
	var source string
	if len(memRows) > 0 && len(sqlRows) > 0 {
		source = readSourceMemorySQLite
	} else if len(sqlRows) > 0 {
		source = readSourceSQLite
	} else {
		source = readSourceMemory
	}
	return deduped, nextCur, source, nil
}

// gatherAggregation routes to SQLite when present — it carries the
// captured_requests↔events join needed for per-row status — and falls back to
// memory otherwise. bucketCount <= 0 returns Total only.
func gatherAggregation(ctx context.Context, q inspect.InspectQuery, bucketCount int, memReader, sqlReader inspect.Reader) (inspect.Aggregation, error) {
	if sqlReader != nil {
		return sqlReader.AggregateRoots(ctx, q, bucketCount)
	}
	if memReader != nil {
		return memReader.AggregateRoots(ctx, q, bucketCount)
	}
	return inspect.Aggregation{}, nil
}

// gatherDetail resolves a record by id, merging siblings across readers.
// Resolution order: memory first; SQLite on miss. Siblings from the other
// reader are merged when the root is found. Returns ErrNotFound when neither
// reader has the record.
func gatherDetail(ctx context.Context, id string, memReader, sqlReader inspect.Reader) (inspect.DetailRecord, error) {
	if memReader != nil {
		detail, err := memReader.ReadDetail(ctx, id)
		if err == nil {
			if sqlReader != nil {
				if sqlDetail, sqlErr := sqlReader.ReadDetail(ctx, id); sqlErr == nil {
					detail = mergeDetailSiblings(detail, sqlDetail)
				}
			}
			return detail, nil
		}
		if !errors.Is(err, inspect.ErrNotFound) {
			return inspect.DetailRecord{}, err
		}
	}
	if sqlReader == nil {
		return inspect.DetailRecord{}, inspect.ErrNotFound
	}
	detail, err := sqlReader.ReadDetail(ctx, id)
	if err != nil {
		return inspect.DetailRecord{}, err
	}
	if memReader != nil {
		if memDetail, memErr := memReader.ReadDetail(ctx, id); memErr == nil {
			detail = mergeDetailSiblings(detail, memDetail)
		}
	}
	return detail, nil
}

// requestsHandler returns an http.HandlerFunc for GET /requests.
// memReader and sqlReader may both be nil (stdout-only configuration).
func requestsHandler(memReader, sqlReader inspect.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, fieldErrs := parseInspectQuery(r.URL.Query())
		if len(fieldErrs) > 0 {
			writeParseErrors(w, fieldErrs)
			return
		}

		ctx := r.Context()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if q.Query.IsUnindexedScan() {
			w.Header().Set("X-Httpcatch-Scan", "leading-wildcard-indexed")
		}

		rows, nextCur, source, err := gatherRoots(ctx, q, memReader, sqlReader)
		if err != nil {
			http.Error(w, fmt.Sprintf("read error: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("X-Httpcatch-Read-Source", source)
		w.WriteHeader(http.StatusOK)
		writeRootsResponse(w, rows, nextCur)
	}
}

// requestsAggregateHandler returns an http.HandlerFunc for
// GET /requests/aggregate. Accepts the same filters as /requests plus an
// optional `buckets` parameter; returns total matching rows and a histogram.
func requestsAggregateHandler(memReader, sqlReader inspect.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vals := r.URL.Query()
		bucketCount := histogramBucketCount
		if bs := vals.Get("buckets"); bs != "" {
			n, err := strconv.Atoi(bs)
			if err != nil || n < 1 || n > maxHistogramBuckets {
				writeFieldError(w, "buckets", fmt.Sprintf("must be an integer between 1 and %d", maxHistogramBuckets))
				return
			}
			bucketCount = n
		}

		q, fieldErrs := parseInspectQuery(vals)
		if len(fieldErrs) > 0 {
			writeParseErrors(w, fieldErrs)
			return
		}
		q.Cursor = nil
		q.Limit = 0

		ctx := r.Context()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		agg, err := gatherAggregation(ctx, q, bucketCount, memReader, sqlReader)
		if err != nil {
			http.Error(w, fmt.Sprintf("read error: %v", err), http.StatusInternalServerError)
			return
		}
		if agg.Buckets == nil {
			agg.Buckets = []inspect.HistogramBucket{}
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(agg)
	}
}

type fieldErrorResponse struct {
	Error string `json:"error"`
	Field string `json:"field"`
}

func writeFieldError(w http.ResponseWriter, field, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(fieldErrorResponse{
		Error: msg,
		Field: field,
	})
}

// writeParseErrors writes a 400 response with the first field error using the
// established single-field envelope. The caller guarantees errs is non-empty.
func writeParseErrors(w http.ResponseWriter, errs []parseFieldError) {
	writeFieldError(w, errs[0].field, errs[0].message)
}

func writeRootsResponse(w http.ResponseWriter, rows []inspect.RootRow, nextCur *inspect.Cursor) {
	if rows == nil {
		rows = []inspect.RootRow{}
	}
	resp := rootsResponse{Records: rows}
	if nextCur != nil {
		s := nextCur.Encode()
		resp.NextCursor = &s
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// requestDetailHandler returns an http.HandlerFunc for GET /requests/{id}.
// memReader and sqlReader may both be nil (stdout-only configuration), in which
// case every call returns 404 — stdout-only mode has no read surface.
func requestDetailHandler(memReader, sqlReader inspect.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		ctx := r.Context()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		if memReader == nil && sqlReader == nil {
			writeDetailNotFound(w, id)
			return
		}

		detail, err := gatherDetail(ctx, id, memReader, sqlReader)
		if errors.Is(err, inspect.ErrNotFound) {
			writeDetailNotFound(w, id)
			return
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("read error: %v", err), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(detail)
	}
}

type detailNotFoundResponse struct {
	Error string `json:"error"`
	ID    string `json:"id"`
}

func writeDetailNotFound(w http.ResponseWriter, id string) {
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(detailNotFoundResponse{
		Error: "record not found",
		ID:    id,
	})
}

// recordMeta is the minimal interface needed to sort sibling records by
// timestamp and id without importing the capture package.
type recordMeta interface {
	RecordTimestamp() time.Time
	RecordID() string
}

// mergeDetailSiblings combines the siblings from two DetailRecords that share
// the same root. The root from primary is kept; siblings from secondary that
// are not already present (by id) are appended, then the merged list is
// re-sorted by timestamp ascending, id ascending.
func mergeDetailSiblings(primary, secondary inspect.DetailRecord) inspect.DetailRecord {
	seen := make(map[string]struct{})
	if rec, ok := primary.Root.(recordMeta); ok {
		seen[rec.RecordID()] = struct{}{}
	}
	for _, e := range primary.Events {
		if rec, ok := e.(recordMeta); ok {
			seen[rec.RecordID()] = struct{}{}
		}
	}

	merged := make([]any, len(primary.Events))
	copy(merged, primary.Events)
	for _, e := range secondary.Events {
		if rec, ok := e.(recordMeta); ok {
			if _, dup := seen[rec.RecordID()]; !dup {
				seen[rec.RecordID()] = struct{}{}
				merged = append(merged, e)
			}
		}
	}

	sort.SliceStable(merged, func(i, j int) bool {
		ri, oki := merged[i].(recordMeta)
		rj, okj := merged[j].(recordMeta)
		if !oki || !okj {
			return false
		}
		ti, tj := ri.RecordTimestamp(), rj.RecordTimestamp()
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return ri.RecordID() < rj.RecordID()
	})

	return inspect.DetailRecord{Root: primary.Root, Events: merged}
}
