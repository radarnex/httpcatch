package searchql

import (
	"fmt"
	"strings"
)

// CompileSQL produces the WHERE-clause fragment and bound arguments for the
// captured-request UNION arm of GET /requests. Column names are prefixed with
// the `cr.` alias used by the SQLite reader. Status terms are emitted via
// CompileSQLHaving instead because they depend on the events aggregate.
func CompileSQL(q Query) (where string, args []any) {
	if len(q.Terms) == 0 {
		return "", nil
	}
	var clauses []string
	for _, t := range q.Terms {
		clause, vals := compileTermSQL(t)
		if clause == "" {
			continue
		}
		clauses = append(clauses, clause)
		args = append(args, vals...)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return strings.Join(clauses, " AND "), args
}

func compileTermSQL(t Term) (string, []any) {
	var pred string
	var argList []any

	switch t.Field {
	case "":
		pred, argList = freeformPredicate(t)
	case FieldHost:
		p, arg := indexedPredicate("cr.host", t)
		pred, argList = p, []any{arg}
	case FieldPath:
		p, arg := indexedPredicate("cr.path", t)
		pred, argList = p, []any{arg}
	case FieldService:
		p, arg := indexedPredicate("cr.service", t)
		pred, argList = p, []any{arg}
	case FieldBody:
		pred = "CAST(cr.body AS TEXT) LIKE ?"
		argList = []any{"%" + t.Value + "%"}
	case FieldHeaders:
		pred, argList = headersAnyPredicate(t)
	case FieldHeader:
		pred, argList = headerNamedPredicate(t)
	case FieldMethod:
		pred = "cr.method = ?"
		argList = []any{t.Value}
	case FieldSourceIP:
		pred = "cr.source_ip = ?"
		argList = []any{t.Value}
	case FieldCorrelationID:
		pred = "cr.correlation_id = ?"
		argList = []any{t.Value}
	case FieldStatus:
		return "", nil
	default:
		return "", nil
	}

	if t.Negated {
		pred = "NOT (" + pred + ")"
	}
	return pred, argList
}

// freeformPredicate emits the Tier-1 union for a freeform term against the
// captured-requests row plus its correlated events. Each Tier-1 arm is
// expressed as its own SELECT inside a UNION subquery: SQLite's planner picks
// the best index per branch (idx_captured_requests_host on the host arm,
// idx_captured_requests_path on the path arm, idx_events_request_path on the
// correlated-event-path arm, and so on) and merges the matching ids before
// probing the outer cr table by id. Scanned dimensions (body, headers across
// both tables) still scan, but each branch is self-contained so the indexed
// branches stay index-backed.
func freeformPredicate(t Term) (string, []any) {
	hostPred, hostArg := indexedPredicate("host", t)
	pathPred, pathArg := indexedPredicate("path", t)
	servicePred, serviceArg := indexedPredicate("service", t)
	eventPathPred, eventPathArg := indexedPredicate("e_ff.request_path", t)
	needle := "%" + t.Value + "%"
	pred := "cr.id IN (" +
		"SELECT id FROM captured_requests WHERE " + hostPred + " UNION " +
		"SELECT id FROM captured_requests WHERE " + pathPred + " UNION " +
		"SELECT id FROM captured_requests WHERE " + servicePred + " UNION " +
		"SELECT id FROM captured_requests WHERE CAST(body AS TEXT) LIKE ? UNION " +
		"SELECT id FROM captured_requests WHERE CAST(headers AS TEXT) LIKE ? UNION " +
		"SELECT cr_ff.id FROM captured_requests cr_ff JOIN events e_ff " +
		"ON e_ff.correlation_id = cr_ff.correlation_id " +
		"WHERE " + eventPathPred + " OR " +
		"CAST(e_ff.request_body AS TEXT) LIKE ? OR " +
		"CAST(e_ff.request_headers AS TEXT) LIKE ? OR " +
		"CAST(e_ff.response_body AS TEXT) LIKE ? OR " +
		"CAST(e_ff.response_headers AS TEXT) LIKE ?" +
		")"
	args := []any{
		hostArg, pathArg, serviceArg,
		needle, needle,
		eventPathArg,
		needle, needle, needle, needle,
	}
	return pred, args
}

// headersAnyPredicate emits a substring predicate against the row's headers
// JSON column plus any correlated events' request/response headers. A row
// matches if any of the three columns substring-matches the needle. The
// headers columns are bound as BLOB at write time (the writer passes a
// json.Marshal []byte), so each comparison wraps the column in
// CAST(... AS TEXT) — without it, SQLite's LIKE returns NULL on a BLOB and
// the predicate silently never matches.
func headersAnyPredicate(t Term) (string, []any) {
	needle := "%" + t.Value + "%"
	pred := "(CAST(cr.headers AS TEXT) LIKE ? OR " +
		"EXISTS (SELECT 1 FROM events e_h " +
		"WHERE e_h.correlation_id = cr.correlation_id " +
		"AND (CAST(e_h.request_headers AS TEXT) LIKE ? OR CAST(e_h.response_headers AS TEXT) LIKE ?)))"
	return pred, []any{needle, needle, needle}
}

// headerNamedPredicate emits a JSON1-backed substring predicate against the
// named header's values across cr.headers, events.request_headers, and
// events.response_headers. json_extract(col, '$."Canonical"') returns a JSON
// array (the http.Header multi-value shape) or NULL when the key is absent;
// json_each iterates the array; the EXISTS chain returns true when any value
// substring-matches the needle. A missing key contributes no match — negation
// of a missing-key row is therefore true, per the PRD. json_extract accepts a
// BLOB column transparently, so no CAST is needed here.
func headerNamedPredicate(t Term) (string, []any) {
	needle := "%" + t.Value + "%"
	path := jsonHeaderPath(t.HeaderName)
	pred := "(" +
		"EXISTS (SELECT 1 FROM json_each(json_extract(cr.headers, ?)) WHERE value LIKE ?) OR " +
		"EXISTS (SELECT 1 FROM events e_h " +
		"WHERE e_h.correlation_id = cr.correlation_id " +
		"AND (" +
		"EXISTS (SELECT 1 FROM json_each(json_extract(e_h.request_headers, ?)) WHERE value LIKE ?) OR " +
		"EXISTS (SELECT 1 FROM json_each(json_extract(e_h.response_headers, ?)) WHERE value LIKE ?)" +
		"))" +
		")"
	return pred, []any{path, needle, path, needle, path, needle}
}

// jsonHeaderPath formats a canonical header name as a SQLite JSON path
// targeting the corresponding key in a `map[string][]string` JSON object.
// Canonical names contain only letters, digits, and hyphens, but the helper
// defensively escapes `\` and `"` so a future change to the canonicaliser
// cannot inject the path string.
func jsonHeaderPath(name string) string {
	escaped := strings.ReplaceAll(name, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return fmt.Sprintf(`$."%s"`, escaped)
}

// indexedPredicate emits the SQL fragment and bound value for an indexed
// dimension term. WildcardNone is exact match; WildcardPrefix is `LIKE 'foo%'`;
// WildcardSubstring is `LIKE '%foo%'`. Quoted values arrive with WildcardNone
// so the literal (including any `*` inside) round-trips to an exact match.
func indexedPredicate(col string, t Term) (string, any) {
	switch t.Wildcard {
	case WildcardPrefix:
		return col + " LIKE ?", t.Value + "%"
	case WildcardSubstring:
		return col + " LIKE ?", "%" + t.Value + "%"
	default:
		return col + " = ?", t.Value
	}
}

// CompileSQLHaving emits the HAVING-clause fragment and arguments for any
// status term in q. status is the only field today whose predicate depends on
// the events aggregation (`MAX(... e.status ...)`), so it lives in HAVING
// rather than WHERE.
func CompileSQLHaving(q Query) (having string, args []any) {
	for _, t := range q.Terms {
		if t.Field != FieldStatus || t.StatusFilter == nil {
			continue
		}
		var pred string
		var vals []any
		if t.StatusFilter.Exact != 0 {
			pred = "MAX(CASE WHEN e.type = 'response' THEN e.status ELSE NULL END) = ?"
			vals = []any{t.StatusFilter.Exact}
		} else {
			lo, hi := t.StatusFilter.ClassRange()
			pred = "MAX(CASE WHEN e.type = 'response' THEN e.status ELSE NULL END) BETWEEN ? AND ?"
			vals = []any{lo, hi}
		}
		if t.Negated {
			pred = "NOT (" + pred + ")"
		}
		return pred, vals
	}
	return "", nil
}

// CompileSQLOrphans emits the WHERE fragment and arguments for the
// orphan-events arm. Only fields whose semantics apply to events on their own
// fields (service, correlation_id, status, headers, header.<name>) participate.
// The caller has already gated this arm on q.HasRequestOnlyFilter() before
// calling.
func CompileSQLOrphans(q Query) (where string, args []any) {
	var clauses []string
	for _, t := range q.Terms {
		switch t.Field {
		case FieldService:
			pred, val := indexedPredicate("e.service", t)
			if t.Negated {
				pred = "NOT (" + pred + ")"
			}
			clauses = append(clauses, pred)
			args = append(args, val)
		case FieldCorrelationID:
			pred := "e.correlation_id = ?"
			if t.Negated {
				pred = "NOT (" + pred + ")"
			}
			clauses = append(clauses, pred)
			args = append(args, t.Value)
		case FieldHeaders:
			needle := "%" + t.Value + "%"
			pred := "(CAST(e.request_headers AS TEXT) LIKE ? OR CAST(e.response_headers AS TEXT) LIKE ?)"
			if t.Negated {
				pred = "NOT (" + pred + ")"
			}
			clauses = append(clauses, pred)
			args = append(args, needle, needle)
		case FieldHeader:
			needle := "%" + t.Value + "%"
			path := jsonHeaderPath(t.HeaderName)
			pred := "(" +
				"EXISTS (SELECT 1 FROM json_each(json_extract(e.request_headers, ?)) WHERE value LIKE ?) OR " +
				"EXISTS (SELECT 1 FROM json_each(json_extract(e.response_headers, ?)) WHERE value LIKE ?)" +
				")"
			if t.Negated {
				pred = "NOT (" + pred + ")"
			}
			clauses = append(clauses, pred)
			args = append(args, path, needle, path, needle)
		case FieldStatus:
			if t.StatusFilter == nil {
				continue
			}
			var pred string
			var vals []any
			if t.StatusFilter.Exact != 0 {
				pred = "e.type = 'response' AND e.status = ?"
				vals = []any{t.StatusFilter.Exact}
			} else {
				lo, hi := t.StatusFilter.ClassRange()
				pred = "e.type = 'response' AND e.status BETWEEN ? AND ?"
				vals = []any{lo, hi}
			}
			if t.Negated {
				pred = "NOT (" + pred + ")"
			}
			clauses = append(clauses, pred)
			args = append(args, vals...)
		case "":
			pred, vals := freeformOrphanPredicate(t)
			if t.Negated {
				pred = "NOT (" + pred + ")"
			}
			clauses = append(clauses, pred)
			args = append(args, vals...)
		}
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return strings.Join(clauses, " AND "), args
}

// freeformOrphanPredicate emits the Tier-1 union for a freeform term against
// an orphan event row. Like freeformPredicate, each indexed Tier-1 arm sits in
// its own UNION branch so the planner uses idx_events_service and
// idx_events_request_path on those branches; scanned body/headers branches
// fall back to a table scan but only when the indexed branches don't satisfy
// the row first.
func freeformOrphanPredicate(t Term) (string, []any) {
	servicePred, serviceArg := indexedPredicate("service", t)
	pathPred, pathArg := indexedPredicate("request_path", t)
	needle := "%" + t.Value + "%"
	pred := "e.id IN (" +
		"SELECT id FROM events WHERE " + servicePred + " UNION " +
		"SELECT id FROM events WHERE " + pathPred + " UNION " +
		"SELECT id FROM events WHERE CAST(request_body AS TEXT) LIKE ? UNION " +
		"SELECT id FROM events WHERE CAST(request_headers AS TEXT) LIKE ? UNION " +
		"SELECT id FROM events WHERE CAST(response_body AS TEXT) LIKE ? UNION " +
		"SELECT id FROM events WHERE CAST(response_headers AS TEXT) LIKE ?" +
		")"
	args := []any{
		serviceArg, pathArg,
		needle, needle, needle, needle,
	}
	return pred, args
}

// HasRequestOnlyTerm reports whether q carries any term whose semantics only
// apply to CapturedRequest rows. Readers use this to exclude orphan rows from
// the UNION when any such term is set — those fields are absent on events.
func (q Query) HasRequestOnlyTerm() bool {
	for _, t := range q.Terms {
		switch t.Field {
		case FieldHost, FieldPath, FieldMethod, FieldSourceIP, FieldBody:
			return true
		}
	}
	return false
}

// HasNonTemporalTerm reports whether q carries any field-qualified term —
// used by the admin layer to route memory-eligible (temporal-only) queries
// to the memory reader.
func (q Query) HasNonTemporalTerm() bool {
	return len(q.Terms) > 0
}
