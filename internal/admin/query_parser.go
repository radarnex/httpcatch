package admin

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/radarnex/httpcatch/internal/inspect"
	"github.com/radarnex/httpcatch/internal/searchql"
)

// knownQueryKeys lists every query parameter GET /requests accepts. Any key
// not in this set triggers a 400. Field-level filters now ride on the single
// `q` parameter and are parsed by the searchql package.
var knownQueryKeys = map[string]struct{}{
	"limit":   {},
	"cursor":  {},
	"since":   {},
	"until":   {},
	"q":       {},
	"live":    {},
	"buckets": {},
}

// parseFieldError is a parse-time validation failure for a single query parameter.
type parseFieldError struct {
	field   string
	message string
}

func (e parseFieldError) Error() string {
	return fmt.Sprintf("%s: %s", e.field, e.message)
}

// parseInspectQuery parses and validates the full set of GET /requests query
// parameters from values. It returns a populated InspectQuery and a slice of
// field-level errors; any non-empty error slice should result in a 400 response.
func parseInspectQuery(values url.Values) (inspect.InspectQuery, []parseFieldError) {
	var errs []parseFieldError

	// Reject unknown keys first so operators get immediate feedback.
	for key := range values {
		if _, ok := knownQueryKeys[key]; !ok {
			errs = append(errs, parseFieldError{
				field:   key,
				message: fmt.Sprintf("unknown query parameter %q", key),
			})
		}
	}
	if len(errs) > 0 {
		return inspect.InspectQuery{}, errs
	}

	q := inspect.InspectQuery{Limit: defaultLimit}

	if ls := values.Get("limit"); ls != "" {
		v, err := strconv.Atoi(ls)
		if err != nil {
			errs = append(errs, parseFieldError{"limit", "must be an integer"})
		} else if v < 1 || v > maxLimit {
			errs = append(errs, parseFieldError{"limit", fmt.Sprintf("must be between 1 and %d", maxLimit)})
		} else {
			q.Limit = v
		}
	}

	if cs := values.Get("cursor"); cs != "" {
		c, err := inspect.DecodeCursor(cs)
		if err != nil {
			errs = append(errs, parseFieldError{"cursor", err.Error()})
		} else {
			q.Cursor = c
		}
	}

	if s := values.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			errs = append(errs, parseFieldError{"since", "must be RFC 3339 (e.g. 2006-01-02T15:04:05Z)"})
		} else {
			q.Since = &t
		}
	}

	if s := values.Get("until"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			errs = append(errs, parseFieldError{"until", "must be RFC 3339 (e.g. 2006-01-02T15:04:05Z)"})
		} else {
			q.Until = &t
		}
	}

	if qs := values.Get("q"); qs != "" {
		parsed, err := searchql.Parse(qs)
		if err != nil {
			var pe *searchql.ParseError
			if errors.As(err, &pe) {
				errs = append(errs, parseFieldError{"q", pe.Error()})
			} else {
				errs = append(errs, parseFieldError{"q", err.Error()})
			}
		} else {
			q.Query = parsed
		}
	}

	return q, errs
}

// hasNonTemporalFilter reports whether q carries any filter that forces a
// SQLite-only read (i.e. any filter that cannot be applied by MemoryReader).
func hasNonTemporalFilter(q inspect.InspectQuery) bool {
	return q.Query.HasNonTemporalTerm()
}
