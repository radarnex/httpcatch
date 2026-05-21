package searchql

import (
	"reflect"
	"strings"
	"testing"
)

func TestCompileSQL_PerKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantSQL  string
		wantArgs []any
	}{
		{
			input:    "service:orders",
			wantSQL:  "cr.service = ?",
			wantArgs: []any{"orders"},
		},
		{
			input:    "host:example.com",
			wantSQL:  "cr.host = ?",
			wantArgs: []any{"example.com"},
		},
		{
			input:    "method:POST",
			wantSQL:  "cr.method = ?",
			wantArgs: []any{"POST"},
		},
		{
			input:    "path:/api/users",
			wantSQL:  "cr.path = ?",
			wantArgs: []any{"/api/users"},
		},
		{
			input:    "source_ip:10.0.0.1",
			wantSQL:  "cr.source_ip = ?",
			wantArgs: []any{"10.0.0.1"},
		},
		{
			input:    "correlation_id:abc",
			wantSQL:  "cr.correlation_id = ?",
			wantArgs: []any{"abc"},
		},
		{
			input:    "body:error",
			wantSQL:  "CAST(cr.body AS TEXT) LIKE ?",
			wantArgs: []any{"%error%"},
		},
		{
			input:    "status:200",
			wantSQL:  "",
			wantArgs: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			gotSQL, gotArgs := CompileSQL(q)
			if gotSQL != tc.wantSQL {
				t.Errorf("SQL: got %q want %q", gotSQL, tc.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("Args: got %v want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

func TestCompileSQL_Wildcards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantSQL  string
		wantArgs []any
	}{
		{"host:billing-api*", "cr.host LIKE ?", []any{"billing-api%"}},
		{"host:*api*", "cr.host LIKE ?", []any{"%api%"}},
		{"host:*api", "cr.host LIKE ?", []any{"%api%"}},
		{"path:/signup*", "cr.path LIKE ?", []any{"/signup%"}},
		{"path:*signup*", "cr.path LIKE ?", []any{"%signup%"}},
		{"service:foo*", "cr.service LIKE ?", []any{"foo%"}},
		{"service:*foo*", "cr.service LIKE ?", []any{"%foo%"}},
		{"body:foo", "CAST(cr.body AS TEXT) LIKE ?", []any{"%foo%"}},
		{"body:foo*", "CAST(cr.body AS TEXT) LIKE ?", []any{"%foo%"}},
		{"body:*foo", "CAST(cr.body AS TEXT) LIKE ?", []any{"%foo%"}},
		{"body:*foo*", "CAST(cr.body AS TEXT) LIKE ?", []any{"%foo%"}},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			gotSQL, gotArgs := CompileSQL(q)
			if gotSQL != tc.wantSQL {
				t.Errorf("SQL: got %q want %q", gotSQL, tc.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("Args: got %v want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

func TestCompileSQL_Quoting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantSQL  string
		wantArgs []any
	}{
		{`host:"foo bar"`, "cr.host = ?", []any{"foo bar"}},
		{`path:"/signup/*"`, "cr.path = ?", []any{"/signup/*"}},
		{`body:"login failed"`, "CAST(cr.body AS TEXT) LIKE ?", []any{"%login failed%"}},
		{`body:"foo\"bar"`, "CAST(cr.body AS TEXT) LIKE ?", []any{`%foo"bar%`}},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			gotSQL, gotArgs := CompileSQL(q)
			if gotSQL != tc.wantSQL {
				t.Errorf("SQL: got %q want %q", gotSQL, tc.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("Args: got %v want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

func TestCompileSQL_Negation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantSQL  string
		wantArgs []any
	}{
		{"-service:foo", "NOT (cr.service = ?)", []any{"foo"}},
		{"-host:billing-api*", "NOT (cr.host LIKE ?)", []any{"billing-api%"}},
		{`-body:"login failed"`, "NOT (CAST(cr.body AS TEXT) LIKE ?)", []any{"%login failed%"}},
		{"-method:GET", "NOT (cr.method = ?)", []any{"GET"}},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			gotSQL, gotArgs := CompileSQL(q)
			if gotSQL != tc.wantSQL {
				t.Errorf("SQL: got %q want %q", gotSQL, tc.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("Args: got %v want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

func TestCompileSQL_MultiTokenAND(t *testing.T) {
	t.Parallel()

	q, err := Parse("service:orders method:POST")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sql, args := CompileSQL(q)
	want := "cr.service = ? AND cr.method = ?"
	if sql != want {
		t.Errorf("SQL: got %q want %q", sql, want)
	}
	if !reflect.DeepEqual(args, []any{"orders", "POST"}) {
		t.Errorf("Args: got %v", args)
	}
}

func TestCompileSQL_MultiTokenWildcardAndNegation(t *testing.T) {
	t.Parallel()

	q, err := Parse("service:foo -method:GET host:billing-api*")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	sql, args := CompileSQL(q)
	want := "cr.service = ? AND NOT (cr.method = ?) AND cr.host LIKE ?"
	if sql != want {
		t.Errorf("SQL: got %q want %q", sql, want)
	}
	if !reflect.DeepEqual(args, []any{"foo", "GET", "billing-api%"}) {
		t.Errorf("Args: got %v", args)
	}
}

func TestCompileSQL_Empty(t *testing.T) {
	t.Parallel()
	sql, args := CompileSQL(Query{})
	if sql != "" {
		t.Errorf("empty Query: SQL %q want empty", sql)
	}
	if args != nil {
		t.Errorf("empty Query: args %v want nil", args)
	}
}

func TestCompileSQL_HeadersAny(t *testing.T) {
	t.Parallel()

	wantSQL := "(CAST(cr.headers AS TEXT) LIKE ? OR " +
		"EXISTS (SELECT 1 FROM events e_h " +
		"WHERE e_h.correlation_id = cr.correlation_id " +
		"AND (CAST(e_h.request_headers AS TEXT) LIKE ? OR CAST(e_h.response_headers AS TEXT) LIKE ?)))"

	tests := []struct {
		input    string
		wantSQL  string
		wantArgs []any
	}{
		{
			input:    "headers:foo",
			wantSQL:  wantSQL,
			wantArgs: []any{"%foo%", "%foo%", "%foo%"},
		},
		{
			// scanned dim — wildcards collapse to substring; still uses same fragment.
			input:    "headers:*foo*",
			wantSQL:  wantSQL,
			wantArgs: []any{"%foo%", "%foo%", "%foo%"},
		},
		{
			input:    "-headers:foo",
			wantSQL:  "NOT (" + wantSQL + ")",
			wantArgs: []any{"%foo%", "%foo%", "%foo%"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			gotSQL, gotArgs := CompileSQL(q)
			if gotSQL != tc.wantSQL {
				t.Errorf("SQL: got %q want %q", gotSQL, tc.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("Args: got %v want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

func TestCompileSQL_HeaderNamed(t *testing.T) {
	t.Parallel()

	wantSQL := "(" +
		"EXISTS (SELECT 1 FROM json_each(json_extract(cr.headers, ?)) WHERE value LIKE ?) OR " +
		"EXISTS (SELECT 1 FROM events e_h " +
		"WHERE e_h.correlation_id = cr.correlation_id " +
		"AND (" +
		"EXISTS (SELECT 1 FROM json_each(json_extract(e_h.request_headers, ?)) WHERE value LIKE ?) OR " +
		"EXISTS (SELECT 1 FROM json_each(json_extract(e_h.response_headers, ?)) WHERE value LIKE ?)" +
		"))" +
		")"
	wantPath := `$."User-Agent"`

	tests := []struct {
		input    string
		wantSQL  string
		wantArgs []any
	}{
		{
			input:    "header.user-agent:client",
			wantSQL:  wantSQL,
			wantArgs: []any{wantPath, "%client%", wantPath, "%client%", wantPath, "%client%"},
		},
		{
			input:    "header.User-Agent:client",
			wantSQL:  wantSQL,
			wantArgs: []any{wantPath, "%client%", wantPath, "%client%", wantPath, "%client%"},
		},
		{
			input:    "header.USER-AGENT:client",
			wantSQL:  wantSQL,
			wantArgs: []any{wantPath, "%client%", wantPath, "%client%", wantPath, "%client%"},
		},
		{
			input:    "-header.X-Trace-Id:abc",
			wantSQL:  "NOT (" + strings.ReplaceAll(wantSQL, `"User-Agent"`, `"X-Trace-Id"`) + ")",
			wantArgs: []any{`$."X-Trace-Id"`, "%abc%", `$."X-Trace-Id"`, "%abc%", `$."X-Trace-Id"`, "%abc%"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			gotSQL, gotArgs := CompileSQL(q)
			if gotSQL != tc.wantSQL {
				t.Errorf("SQL: got %q want %q", gotSQL, tc.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("Args: got %v want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

func TestCompileSQL_HeaderQuotedPhrase(t *testing.T) {
	t.Parallel()

	q, err := Parse(`header.user-agent:"Mozilla/5.0 (Macintosh)"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	_, gotArgs := CompileSQL(q)
	wantArgs := []any{
		`$."User-Agent"`, "%Mozilla/5.0 (Macintosh)%",
		`$."User-Agent"`, "%Mozilla/5.0 (Macintosh)%",
		`$."User-Agent"`, "%Mozilla/5.0 (Macintosh)%",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("Args: got %v want %v", gotArgs, wantArgs)
	}
}

func TestCompileSQL_HeadersCombineWithOtherTerms(t *testing.T) {
	t.Parallel()

	q, err := Parse("service:foo -header.user-agent:bot host:billing-api*")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	gotSQL, gotArgs := CompileSQL(q)
	if !strings.Contains(gotSQL, "cr.service = ?") {
		t.Errorf("missing service clause: %q", gotSQL)
	}
	if !strings.Contains(gotSQL, "cr.host LIKE ?") {
		t.Errorf("missing host clause: %q", gotSQL)
	}
	if !strings.Contains(gotSQL, "NOT ((EXISTS (SELECT 1 FROM json_each(json_extract(cr.headers,") {
		t.Errorf("missing negated header.user-agent clause: %q", gotSQL)
	}
	// First arg is service, then 3x json header pairs, then host.
	if gotArgs[0] != "foo" {
		t.Errorf("first arg: got %v want %q", gotArgs[0], "foo")
	}
	if gotArgs[len(gotArgs)-1] != "billing-api%" {
		t.Errorf("last arg: got %v want %q", gotArgs[len(gotArgs)-1], "billing-api%")
	}
}

func TestCompileSQL_StatusOnly_NoWhere(t *testing.T) {
	t.Parallel()
	q, _ := Parse("status:5xx")
	sql, _ := CompileSQL(q)
	if sql != "" {
		t.Errorf("status-only: SQL %q want empty", sql)
	}
}

func TestCompileSQLHaving_StatusExact(t *testing.T) {
	t.Parallel()
	q, _ := Parse("status:200")
	having, args := CompileSQLHaving(q)
	want := "MAX(CASE WHEN e.type = 'response' THEN e.status ELSE NULL END) = ?"
	if having != want {
		t.Errorf("HAVING: got %q want %q", having, want)
	}
	if !reflect.DeepEqual(args, []any{200}) {
		t.Errorf("Args: got %v", args)
	}
}

func TestCompileSQLHaving_StatusClass(t *testing.T) {
	t.Parallel()
	q, _ := Parse("status:5xx")
	having, args := CompileSQLHaving(q)
	want := "MAX(CASE WHEN e.type = 'response' THEN e.status ELSE NULL END) BETWEEN ? AND ?"
	if having != want {
		t.Errorf("HAVING: got %q want %q", having, want)
	}
	if !reflect.DeepEqual(args, []any{500, 599}) {
		t.Errorf("Args: got %v", args)
	}
}

func TestCompileSQLHaving_StatusNegated(t *testing.T) {
	t.Parallel()
	q, _ := Parse("-status:200")
	having, args := CompileSQLHaving(q)
	want := "NOT (MAX(CASE WHEN e.type = 'response' THEN e.status ELSE NULL END) = ?)"
	if having != want {
		t.Errorf("HAVING: got %q want %q", having, want)
	}
	if !reflect.DeepEqual(args, []any{200}) {
		t.Errorf("Args: got %v", args)
	}
}

func TestCompileSQLHaving_NoStatus(t *testing.T) {
	t.Parallel()
	q, _ := Parse("service:orders")
	having, args := CompileSQLHaving(q)
	if having != "" || args != nil {
		t.Errorf("non-status query: having %q args %v want empty/nil", having, args)
	}
}

func TestCompileSQLOrphans_PassesService_AndStatus(t *testing.T) {
	t.Parallel()
	q, _ := Parse("service:orders status:5xx")
	sql, args := CompileSQLOrphans(q)
	want := "e.service = ? AND e.type = 'response' AND e.status BETWEEN ? AND ?"
	if sql != want {
		t.Errorf("orphan SQL: got %q want %q", sql, want)
	}
	if !reflect.DeepEqual(args, []any{"orders", 500, 599}) {
		t.Errorf("Args: got %v", args)
	}
}

func TestCompileSQLOrphans_ServiceWildcard(t *testing.T) {
	t.Parallel()
	q, _ := Parse("service:foo*")
	sql, args := CompileSQLOrphans(q)
	want := "e.service LIKE ?"
	if sql != want {
		t.Errorf("orphan SQL: got %q want %q", sql, want)
	}
	if !reflect.DeepEqual(args, []any{"foo%"}) {
		t.Errorf("Args: got %v", args)
	}
}

func TestCompileSQLOrphans_Negated(t *testing.T) {
	t.Parallel()
	q, _ := Parse("-service:foo -status:200")
	sql, args := CompileSQLOrphans(q)
	want := "NOT (e.service = ?) AND NOT (e.type = 'response' AND e.status = ?)"
	if sql != want {
		t.Errorf("orphan SQL: got %q want %q", sql, want)
	}
	if !reflect.DeepEqual(args, []any{"foo", 200}) {
		t.Errorf("Args: got %v", args)
	}
}

func TestCompileSQLOrphans_HeadersAny(t *testing.T) {
	t.Parallel()
	q, _ := Parse("headers:foo")
	sql, args := CompileSQLOrphans(q)
	want := "(CAST(e.request_headers AS TEXT) LIKE ? OR CAST(e.response_headers AS TEXT) LIKE ?)"
	if sql != want {
		t.Errorf("orphan SQL: got %q want %q", sql, want)
	}
	if !reflect.DeepEqual(args, []any{"%foo%", "%foo%"}) {
		t.Errorf("Args: got %v", args)
	}
}

func TestCompileSQLOrphans_HeaderNamed(t *testing.T) {
	t.Parallel()
	q, _ := Parse("header.user-agent:client")
	sql, args := CompileSQLOrphans(q)
	want := "(" +
		"EXISTS (SELECT 1 FROM json_each(json_extract(e.request_headers, ?)) WHERE value LIKE ?) OR " +
		"EXISTS (SELECT 1 FROM json_each(json_extract(e.response_headers, ?)) WHERE value LIKE ?)" +
		")"
	if sql != want {
		t.Errorf("orphan SQL: got %q want %q", sql, want)
	}
	wantArgs := []any{`$."User-Agent"`, "%client%", `$."User-Agent"`, "%client%"}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Errorf("Args: got %v want %v", args, wantArgs)
	}
}

func TestCompileSQLOrphans_HeaderNamed_Negated(t *testing.T) {
	t.Parallel()
	q, _ := Parse("-header.x-trace-id:abc")
	sql, _ := CompileSQLOrphans(q)
	if !strings.HasPrefix(sql, "NOT (") {
		t.Errorf("orphan SQL should be negated: %q", sql)
	}
}

// freeformReqWantSQL returns the expected captured-request UNION SQL for a
// freeform term with the given indexed predicates (host/path/service/event
// request_path). Scanned arms always use substring LIKE.
func freeformReqWantSQL(hostPred, pathPred, servicePred, eventPathPred string) string {
	return "cr.id IN (" +
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
}

func freeformOrphanWantSQL(servicePred, pathPred string) string {
	return "e.id IN (" +
		"SELECT id FROM events WHERE " + servicePred + " UNION " +
		"SELECT id FROM events WHERE " + pathPred + " UNION " +
		"SELECT id FROM events WHERE CAST(request_body AS TEXT) LIKE ? UNION " +
		"SELECT id FROM events WHERE CAST(request_headers AS TEXT) LIKE ? UNION " +
		"SELECT id FROM events WHERE CAST(response_body AS TEXT) LIKE ? UNION " +
		"SELECT id FROM events WHERE CAST(response_headers AS TEXT) LIKE ?" +
		")"
}

func TestCompileSQL_FreeformExact(t *testing.T) {
	t.Parallel()
	q, err := Parse("billing-api")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	gotSQL, gotArgs := CompileSQL(q)
	wantSQL := freeformReqWantSQL("host = ?", "path = ?", "service = ?", "e_ff.request_path = ?")
	if gotSQL != wantSQL {
		t.Errorf("SQL: got %q\nwant %q", gotSQL, wantSQL)
	}
	wantArgs := []any{
		"billing-api", "billing-api", "billing-api",
		"%billing-api%", "%billing-api%",
		"billing-api",
		"%billing-api%", "%billing-api%", "%billing-api%", "%billing-api%",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("Args: got %v want %v", gotArgs, wantArgs)
	}
}

func TestCompileSQL_FreeformPrefix(t *testing.T) {
	t.Parallel()
	q, _ := Parse("billing-api*")
	gotSQL, gotArgs := CompileSQL(q)
	wantSQL := freeformReqWantSQL("host LIKE ?", "path LIKE ?", "service LIKE ?", "e_ff.request_path LIKE ?")
	if gotSQL != wantSQL {
		t.Errorf("SQL: got %q\nwant %q", gotSQL, wantSQL)
	}
	wantArgs := []any{
		"billing-api%", "billing-api%", "billing-api%",
		"%billing-api%", "%billing-api%",
		"billing-api%",
		"%billing-api%", "%billing-api%", "%billing-api%", "%billing-api%",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("Args: got %v want %v", gotArgs, wantArgs)
	}
}

func TestCompileSQL_FreeformSubstring(t *testing.T) {
	t.Parallel()
	q, _ := Parse("*billing*")
	gotSQL, gotArgs := CompileSQL(q)
	wantSQL := freeformReqWantSQL("host LIKE ?", "path LIKE ?", "service LIKE ?", "e_ff.request_path LIKE ?")
	if gotSQL != wantSQL {
		t.Errorf("SQL: got %q\nwant %q", gotSQL, wantSQL)
	}
	wantArgs := []any{
		"%billing%", "%billing%", "%billing%",
		"%billing%", "%billing%",
		"%billing%",
		"%billing%", "%billing%", "%billing%", "%billing%",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("Args: got %v want %v", gotArgs, wantArgs)
	}
}

func TestCompileSQL_FreeformNegated(t *testing.T) {
	t.Parallel()
	q, _ := Parse("-billing-api")
	gotSQL, _ := CompileSQL(q)
	wantSQL := "NOT (" + freeformReqWantSQL("host = ?", "path = ?", "service = ?", "e_ff.request_path = ?") + ")"
	if gotSQL != wantSQL {
		t.Errorf("SQL: got %q\nwant %q", gotSQL, wantSQL)
	}
}

func TestCompileSQL_FreeformQuoted(t *testing.T) {
	t.Parallel()
	q, _ := Parse(`"login failed"`)
	gotSQL, gotArgs := CompileSQL(q)
	wantSQL := freeformReqWantSQL("host = ?", "path = ?", "service = ?", "e_ff.request_path = ?")
	if gotSQL != wantSQL {
		t.Errorf("SQL: got %q\nwant %q", gotSQL, wantSQL)
	}
	wantArgs := []any{
		"login failed", "login failed", "login failed",
		"%login failed%", "%login failed%",
		"login failed",
		"%login failed%", "%login failed%", "%login failed%", "%login failed%",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("Args: got %v want %v", gotArgs, wantArgs)
	}
}

func TestCompileSQL_FreeformMixedWithFieldQualified(t *testing.T) {
	t.Parallel()
	q, _ := Parse("service:billing-svc orders")
	gotSQL, gotArgs := CompileSQL(q)
	wantSQL := "cr.service = ? AND " + freeformReqWantSQL("host = ?", "path = ?", "service = ?", "e_ff.request_path = ?")
	if gotSQL != wantSQL {
		t.Errorf("SQL: got %q\nwant %q", gotSQL, wantSQL)
	}
	wantArgs := []any{
		"billing-svc",
		"orders", "orders", "orders",
		"%orders%", "%orders%",
		"orders",
		"%orders%", "%orders%", "%orders%", "%orders%",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("Args: got %v want %v", gotArgs, wantArgs)
	}
}

func TestCompileSQL_FreeformMultiToken(t *testing.T) {
	t.Parallel()
	q, _ := Parse("billing-api orders")
	gotSQL, _ := CompileSQL(q)
	wantSQL := freeformReqWantSQL("host = ?", "path = ?", "service = ?", "e_ff.request_path = ?") +
		" AND " +
		freeformReqWantSQL("host = ?", "path = ?", "service = ?", "e_ff.request_path = ?")
	if gotSQL != wantSQL {
		t.Errorf("SQL: got %q\nwant %q", gotSQL, wantSQL)
	}
}

func TestCompileSQLOrphans_Freeform(t *testing.T) {
	t.Parallel()
	q, _ := Parse("billing-api*")
	gotSQL, gotArgs := CompileSQLOrphans(q)
	wantSQL := freeformOrphanWantSQL("service LIKE ?", "request_path LIKE ?")
	if gotSQL != wantSQL {
		t.Errorf("orphan SQL: got %q\nwant %q", gotSQL, wantSQL)
	}
	wantArgs := []any{
		"billing-api%", "billing-api%",
		"%billing-api%", "%billing-api%", "%billing-api%", "%billing-api%",
	}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("Args: got %v want %v", gotArgs, wantArgs)
	}
}

func TestCompileSQLOrphans_FreeformNegated(t *testing.T) {
	t.Parallel()
	q, _ := Parse("-billing-api")
	gotSQL, _ := CompileSQLOrphans(q)
	if !strings.HasPrefix(gotSQL, "NOT (") {
		t.Errorf("orphan SQL should be negated: %q", gotSQL)
	}
}

func TestCompileSQLOrphans_IgnoresRequestOnly(t *testing.T) {
	t.Parallel()
	q, _ := Parse("method:GET path:/api host:h source_ip:1 body:e correlation_id:c")
	sql, args := CompileSQLOrphans(q)
	want := "e.correlation_id = ?"
	if sql != want {
		t.Errorf("orphan SQL: got %q want %q", sql, want)
	}
	if !reflect.DeepEqual(args, []any{"c"}) {
		t.Errorf("Args: got %v", args)
	}
}
