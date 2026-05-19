package admin

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/radarnex/httpcatch/internal/inspect"
)

// canonicalHTTPMethods is the set of valid HTTP method names. Methods are
// stored uppercase; the parser normalises the input before validating.
var canonicalHTTPMethods = map[string]struct{}{
	"GET":     {},
	"HEAD":    {},
	"POST":    {},
	"PUT":     {},
	"DELETE":  {},
	"CONNECT": {},
	"OPTIONS": {},
	"TRACE":   {},
	"PATCH":   {},
}

// knownQueryKeys lists every query parameter GET /requests accepts. Any key
// not in this set triggers a 400.
var knownQueryKeys = map[string]struct{}{
	"limit":          {},
	"cursor":         {},
	"since":          {},
	"until":          {},
	"service":        {},
	"method":         {},
	"status":         {},
	"path":           {},
	"correlation_id": {},
	"source_ip":      {},
	"has_events":     {},
	"body":           {},
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

	// limit
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

	// cursor
	if cs := values.Get("cursor"); cs != "" {
		c, err := inspect.DecodeCursor(cs)
		if err != nil {
			errs = append(errs, parseFieldError{"cursor", err.Error()})
		} else {
			q.Cursor = c
		}
	}

	// since
	if s := values.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			errs = append(errs, parseFieldError{"since", "must be RFC 3339 (e.g. 2006-01-02T15:04:05Z)"})
		} else {
			q.Since = &t
		}
	}

	// until
	if s := values.Get("until"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			errs = append(errs, parseFieldError{"until", "must be RFC 3339 (e.g. 2006-01-02T15:04:05Z)"})
		} else {
			q.Until = &t
		}
	}

	if s := values.Get("service"); s != "" {
		q.Service = s
	}

	// method
	if s := values.Get("method"); s != "" {
		upper := strings.ToUpper(s)
		if _, ok := canonicalHTTPMethods[upper]; !ok {
			errs = append(errs, parseFieldError{"method", fmt.Sprintf("unknown HTTP method %q", s)})
		} else {
			q.Method = upper
		}
	}

	// status
	if s := values.Get("status"); s != "" {
		sf, err := parseStatusFilter(s)
		if err != nil {
			errs = append(errs, parseFieldError{"status", err.Error()})
		} else {
			q.Status = sf
		}
	}

	if s := values.Get("path"); s != "" {
		q.Path = s
	}

	if s := values.Get("correlation_id"); s != "" {
		q.CorrelationID = s
	}

	if s := values.Get("source_ip"); s != "" {
		q.SourceIP = s
	}

	if s := values.Get("body"); s != "" {
		q.Body = s
	}

	// has_events
	if s := values.Get("has_events"); s != "" {
		switch s {
		case "true":
			v := true
			q.HasEvents = &v
		case "false":
			v := false
			q.HasEvents = &v
		default:
			errs = append(errs, parseFieldError{"has_events", `must be "true" or "false"`})
		}
	}

	return q, errs
}

// parseStatusFilter parses a status string that is either an exact integer
// (e.g. "200") or a class form (e.g. "2xx", "5xx"). Returns an error when
// the string matches neither form.
func parseStatusFilter(s string) (*inspect.StatusFilter, error) {
	// Class form: digit followed by "xx", e.g. "2xx".
	if len(s) == 3 && s[1] == 'x' && s[2] == 'x' {
		d := s[0]
		if d >= '1' && d <= '5' {
			return &inspect.StatusFilter{Class: s}, nil
		}
	}

	// Exact integer.
	v, err := strconv.Atoi(s)
	if err != nil || v < 100 || v > 599 {
		return nil, fmt.Errorf("must be an integer status code (e.g. 200) or class form (e.g. 2xx, 5xx)")
	}
	return &inspect.StatusFilter{Exact: v}, nil
}

// hasNonTemporalFilter reports whether q carries any filter that forces a
// SQLite-only read (i.e. any filter that cannot be applied by MemoryReader).
func hasNonTemporalFilter(q inspect.InspectQuery) bool {
	return q.Service != "" ||
		q.CorrelationID != "" ||
		q.Status != nil ||
		q.HasRequestOnlyFilter()
}
