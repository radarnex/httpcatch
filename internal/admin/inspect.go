package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"

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
		limit, cursor, ok := parseRequestsParams(w, r)
		if !ok {
			return
		}

		ctx := r.Context()
		q := inspect.InspectQuery{Limit: limit, Cursor: cursor}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")

		if memReader == nil && sqlReader == nil {
			w.Header().Set("X-Httpcatch-Read-Source", readSourceNone)
			w.WriteHeader(http.StatusOK)
			writeRootsResponse(w, nil, nil)
			return
		}

		var (
			rows      []inspect.RootRow
			nextCur   *inspect.Cursor
			source    string
		)

		if memReader != nil {
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
		} else {
			// SQLite only.
			sqlRows, sqlNext, err := sqlReader.ReadRoots(ctx, q, limit, cursor)
			if err != nil {
				http.Error(w, fmt.Sprintf("sqlite read error: %v", err), http.StatusInternalServerError)
				return
			}
			rows = sqlRows
			nextCur = sqlNext
			source = readSourceSQLite
		}

		w.Header().Set("X-Httpcatch-Read-Source", source)
		w.WriteHeader(http.StatusOK)
		writeRootsResponse(w, rows, nextCur)
	}
}

// parseRequestsParams parses and validates limit and cursor from the request
// URL. Returns false (after writing a 400 response) on any validation failure.
func parseRequestsParams(w http.ResponseWriter, r *http.Request) (limit int, cursor *inspect.Cursor, ok bool) {
	limit = defaultLimit

	if ls := r.URL.Query().Get("limit"); ls != "" {
		v, err := strconv.Atoi(ls)
		if err != nil {
			writeFieldError(w, "limit", "must be an integer")
			return 0, nil, false
		}
		if v < 1 || v > maxLimit {
			writeFieldError(w, "limit", fmt.Sprintf("must be between 1 and %d", maxLimit))
			return 0, nil, false
		}
		limit = v
	}

	if cs := r.URL.Query().Get("cursor"); cs != "" {
		c, err := inspect.DecodeCursor(cs)
		if err != nil {
			writeFieldError(w, "cursor", err.Error())
			return 0, nil, false
		}
		cursor = c
	}

	return limit, cursor, true
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
