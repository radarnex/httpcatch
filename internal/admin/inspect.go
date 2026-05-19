package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
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
)

type rootsResponse struct {
	Records    []inspect.RootRow `json:"records"`
	NextCursor *string           `json:"next_cursor"`
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

		limit := q.Limit
		cursor := q.Cursor
		ctx := r.Context()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		if memReader == nil && sqlReader == nil {
			w.Header().Set("X-Httpcatch-Read-Source", readSourceNone)
			w.WriteHeader(http.StatusOK)
			writeRootsResponse(w, nil, nil)
			return
		}

		var (
			rows    []inspect.RootRow
			nextCur *inspect.Cursor
			source  string
		)

		// Any non-temporal filter forces SQLite-only reads. Memory cannot apply
		// joins (e.g. status via the events table) and filtered queries must not
		// silently miss records that aged out of the ring buffer.
		if hasNonTemporalFilter(q) || memReader == nil {
			if sqlReader == nil {
				// No SQLite available; return empty list (e.g. stdout-only with filters).
				w.Header().Set("X-Httpcatch-Read-Source", readSourceNone)
				w.WriteHeader(http.StatusOK)
				writeRootsResponse(w, nil, nil)
				return
			}
			sqlRows, sqlNext, err := sqlReader.ReadRoots(ctx, q, limit, cursor)
			if err != nil {
				http.Error(w, fmt.Sprintf("sqlite read error: %v", err), http.StatusInternalServerError)
				return
			}
			rows = sqlRows
			nextCur = sqlNext
			source = readSourceSQLite
		} else {
			// Memory-eligible path: temporal-only or no filters.
			memRows, memNext, err := memReader.ReadRoots(ctx, q, limit, cursor)
			if err != nil {
				http.Error(w, fmt.Sprintf("memory read error: %v", err), http.StatusInternalServerError)
				return
			}
			if len(memRows) >= limit || sqlReader == nil {
				rows = memRows
				nextCur = memNext
				source = readSourceMemory
			} else {
				// Memory yielded fewer rows than requested; fall through to SQLite
				// for the full page. Deduplicate by id across both sets, re-sort,
				// then trim to limit.
				memIDs := make(map[string]struct{}, len(memRows))
				for _, row := range memRows {
					memIDs[row.ID] = struct{}{}
				}
				sqlRows, _, err := sqlReader.ReadRoots(ctx, q, limit, cursor)
				if err != nil {
					http.Error(w, fmt.Sprintf("sqlite read error: %v", err), http.StatusInternalServerError)
					return
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
				if len(deduped) > limit {
					last := deduped[limit-1]
					nextCur = &inspect.Cursor{Timestamp: last.Timestamp, ID: last.ID}
					deduped = deduped[:limit]
				}
				rows = deduped
				if len(memRows) > 0 && len(sqlRows) > 0 {
					source = readSourceMemorySQLite
				} else if len(sqlRows) > 0 {
					source = readSourceSQLite
				} else {
					source = readSourceMemory
				}
			}
		}

		w.Header().Set("X-Httpcatch-Read-Source", source)
		w.WriteHeader(http.StatusOK)
		writeRootsResponse(w, rows, nextCur)
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
//
// Resolution order: memory is tried first. If memory finds the root, sibling
// gathering also pulls from SQLite (when enabled) and deduplicates by id. If
// memory does not find the root, SQLite is consulted.
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

		// Try memory first.
		if memReader != nil {
			detail, err := memReader.ReadDetail(ctx, id)
			if err == nil {
				if sqlReader != nil {
					sqlDetail, sqlErr := sqlReader.ReadDetail(ctx, id)
					if sqlErr == nil {
						detail = mergeDetailSiblings(detail, sqlDetail)
					}
					// If SQLite returns not-found or another error, the memory
					// result is used as-is; SQLite may simply not have the record.
				}
				w.WriteHeader(http.StatusOK)
				_ = json.NewEncoder(w).Encode(detail)
				return
			}
			if !errors.Is(err, inspect.ErrNotFound) {
				http.Error(w, fmt.Sprintf("memory read error: %v", err), http.StatusInternalServerError)
				return
			}
		}

		// Memory did not find the root (or memory is disabled). Fall through to SQLite.
		if sqlReader == nil {
			writeDetailNotFound(w, id)
			return
		}
		detail, err := sqlReader.ReadDetail(ctx, id)
		if errors.Is(err, inspect.ErrNotFound) {
			writeDetailNotFound(w, id)
			return
		}
		if err != nil {
			http.Error(w, fmt.Sprintf("sqlite read error: %v", err), http.StatusInternalServerError)
			return
		}
		// SQLite found the root. Merge any siblings that memory holds.
		if memReader != nil {
			memDetail, memErr := memReader.ReadDetail(ctx, id)
			if memErr == nil {
				detail = mergeDetailSiblings(detail, memDetail)
			}
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
