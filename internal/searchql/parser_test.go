package searchql

import (
	"strings"
	"testing"
)

func TestParse_AcceptedKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  Term
	}{
		{"service:orders", Term{Field: FieldService, Value: "orders"}},
		{"host:example.com", Term{Field: FieldHost, Value: "example.com"}},
		{"path:/api/users", Term{Field: FieldPath, Value: "/api/users"}},
		{"method:get", Term{Field: FieldMethod, Value: "GET"}},
		{"method:POST", Term{Field: FieldMethod, Value: "POST"}},
		{"source_ip:10.0.0.1", Term{Field: FieldSourceIP, Value: "10.0.0.1"}},
		{"correlation_id:abc-123", Term{Field: FieldCorrelationID, Value: "abc-123"}},
		{"body:error", Term{Field: FieldBody, Value: "error"}},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.input, err)
			}
			if len(q.Terms) != 1 {
				t.Fatalf("Parse(%q): got %d terms want 1", tc.input, len(q.Terms))
			}
			got := q.Terms[0]
			if got.Field != tc.want.Field {
				t.Errorf("Field: got %q want %q", got.Field, tc.want.Field)
			}
			if got.Value != tc.want.Value {
				t.Errorf("Value: got %q want %q", got.Value, tc.want.Value)
			}
		})
	}
}

func TestParse_HeadersKeyword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantVal  string
		wantWild Wildcard
	}{
		{"headers:foo", "foo", WildcardNone},
		{"headers:foo*", "foo", WildcardPrefix},
		{"headers:*foo", "foo", WildcardSubstring},
		{"headers:*foo*", "foo", WildcardSubstring},
		{"HEADERS:foo", "foo", WildcardNone},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.input, err)
			}
			if len(q.Terms) != 1 {
				t.Fatalf("got %d terms want 1", len(q.Terms))
			}
			got := q.Terms[0]
			if got.Field != FieldHeaders {
				t.Errorf("Field: got %q want %q", got.Field, FieldHeaders)
			}
			if got.Value != tc.wantVal {
				t.Errorf("Value: got %q want %q", got.Value, tc.wantVal)
			}
			if got.Wildcard != tc.wantWild {
				t.Errorf("Wildcard: got %v want %v", got.Wildcard, tc.wantWild)
			}
			if got.HeaderName != "" {
				t.Errorf("HeaderName: got %q want empty", got.HeaderName)
			}
		})
	}
}

func TestParse_HeaderNamed_Canonicalises(t *testing.T) {
	t.Parallel()

	// All three spellings of the same header name must produce the same
	// canonical HeaderName in the AST.
	inputs := []string{
		"header.User-Agent:client",
		"header.user-agent:client",
		"header.USER-AGENT:client",
		"HEADER.user-agent:client",
	}
	for _, in := range inputs {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", in, err)
			}
			if len(q.Terms) != 1 {
				t.Fatalf("got %d terms want 1", len(q.Terms))
			}
			got := q.Terms[0]
			if got.Field != FieldHeader {
				t.Errorf("Field: got %q want %q", got.Field, FieldHeader)
			}
			if got.HeaderName != "User-Agent" {
				t.Errorf("HeaderName: got %q want %q", got.HeaderName, "User-Agent")
			}
			if got.Value != "client" {
				t.Errorf("Value: got %q want %q", got.Value, "client")
			}
		})
	}
}

func TestParse_HeaderNamed_WildcardsOnValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantVal  string
		wantWild Wildcard
	}{
		{"header.X-Trace-Id:abc", "abc", WildcardNone},
		{"header.X-Trace-Id:abc*", "abc", WildcardPrefix},
		{"header.X-Trace-Id:*abc", "abc", WildcardSubstring},
		{"header.X-Trace-Id:*abc*", "abc", WildcardSubstring},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.input, err)
			}
			got := q.Terms[0]
			if got.Value != tc.wantVal {
				t.Errorf("Value: got %q want %q", got.Value, tc.wantVal)
			}
			if got.Wildcard != tc.wantWild {
				t.Errorf("Wildcard: got %v want %v", got.Wildcard, tc.wantWild)
			}
		})
	}
}

func TestParse_HeaderNamed_QuotedAndNegated(t *testing.T) {
	t.Parallel()

	q, err := Parse(`-header.User-Agent:"Mozilla/5.0 (Macintosh)"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Terms) != 1 {
		t.Fatalf("got %d terms want 1", len(q.Terms))
	}
	got := q.Terms[0]
	if !got.Negated {
		t.Errorf("Negated: got false want true")
	}
	if got.Field != FieldHeader || got.HeaderName != "User-Agent" {
		t.Errorf("Field/HeaderName: got %q/%q", got.Field, got.HeaderName)
	}
	if got.Value != "Mozilla/5.0 (Macintosh)" {
		t.Errorf("Value: got %q want %q", got.Value, "Mozilla/5.0 (Macintosh)")
	}
	if !got.QuotedLiteral {
		t.Errorf("QuotedLiteral: got false want true")
	}
}

func TestParse_HeaderNamed_ErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantToken string
		wantMsg   string
	}{
		{
			name:      "wildcard in header name",
			input:     "header.x-*:foo",
			wantToken: "header.x-*:foo",
			wantMsg:   "wildcards are not supported in header name",
		},
		{
			name:      "leading wildcard in header name",
			input:     "header.*trace:foo",
			wantToken: "header.*trace:foo",
			wantMsg:   "wildcards are not supported in header name",
		},
		{
			name:      "empty header name",
			input:     "header.:foo",
			wantToken: "header.:foo",
			wantMsg:   "empty header name",
		},
		{
			name:      "empty value after header.<name>:",
			input:     "header.User-Agent:",
			wantToken: "header.User-Agent:",
			wantMsg:   "empty value",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tc.input)
			if err == nil {
				t.Fatalf("Parse(%q): expected error", tc.input)
			}
			pe, ok := err.(*ParseError)
			if !ok {
				t.Fatalf("expected *ParseError, got %T (%v)", err, err)
			}
			if pe.Token != tc.wantToken {
				t.Errorf("Token: got %q want %q", pe.Token, tc.wantToken)
			}
			if !strings.Contains(pe.Message, tc.wantMsg) {
				t.Errorf("Message %q does not contain %q", pe.Message, tc.wantMsg)
			}
		})
	}
}

func TestParse_Status_Exact(t *testing.T) {
	t.Parallel()
	q, err := Parse("status:200")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Terms) != 1 {
		t.Fatalf("got %d terms want 1", len(q.Terms))
	}
	sf := q.Terms[0].StatusFilter
	if sf == nil || sf.Exact != 200 {
		t.Errorf("status filter: got %+v want Exact=200", sf)
	}
}

func TestParse_Status_Class(t *testing.T) {
	t.Parallel()
	for _, cls := range []string{"1xx", "2xx", "3xx", "4xx", "5xx"} {
		q, err := Parse("status:" + cls)
		if err != nil {
			t.Fatalf("Parse status:%s: %v", cls, err)
		}
		sf := q.Terms[0].StatusFilter
		if sf == nil || sf.Class != cls {
			t.Errorf("status:%s: got %+v want Class=%s", cls, sf, cls)
		}
	}
}

func TestParse_MultiTokenAND(t *testing.T) {
	t.Parallel()

	q, err := Parse("service:orders method:POST path:/api")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Terms) != 3 {
		t.Fatalf("got %d terms want 3", len(q.Terms))
	}
	wantFields := []Field{FieldService, FieldMethod, FieldPath}
	for i, f := range wantFields {
		if q.Terms[i].Field != f {
			t.Errorf("term[%d].Field: got %q want %q", i, q.Terms[i].Field, f)
		}
	}
}

func TestParse_Empty(t *testing.T) {
	t.Parallel()
	q, err := Parse("")
	if err != nil {
		t.Fatalf("Parse empty: %v", err)
	}
	if len(q.Terms) != 0 {
		t.Errorf("got %d terms want 0", len(q.Terms))
	}

	q, err = Parse("   ")
	if err != nil {
		t.Fatalf("Parse whitespace: %v", err)
	}
	if len(q.Terms) != 0 {
		t.Errorf("got %d terms want 0", len(q.Terms))
	}
}

func TestParse_Wildcards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantVal  string
		wantWild Wildcard
	}{
		{"host:billing-api", "billing-api", WildcardNone},
		{"host:billing-api*", "billing-api", WildcardPrefix},
		{"host:*api", "api", WildcardSubstring},
		{"host:*api*", "api", WildcardSubstring},
		{"path:/signup", "/signup", WildcardNone},
		{"path:/signup*", "/signup", WildcardPrefix},
		{"service:foo*", "foo", WildcardPrefix},
		{"service:*foo*", "foo", WildcardSubstring},
		{"body:foo", "foo", WildcardNone},
		{"body:foo*", "foo", WildcardPrefix},
		{"body:*foo", "foo", WildcardSubstring},
		{"body:*foo*", "foo", WildcardSubstring},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.input, err)
			}
			if len(q.Terms) != 1 {
				t.Fatalf("got %d terms want 1", len(q.Terms))
			}
			got := q.Terms[0]
			if got.Value != tc.wantVal {
				t.Errorf("Value: got %q want %q", got.Value, tc.wantVal)
			}
			if got.Wildcard != tc.wantWild {
				t.Errorf("Wildcard: got %v want %v", got.Wildcard, tc.wantWild)
			}
			if got.QuotedLiteral {
				t.Errorf("QuotedLiteral: got true want false")
			}
		})
	}
}

func TestParse_Quoting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input   string
		wantVal string
	}{
		{`host:"foo bar"`, "foo bar"},
		{`path:"/signup/*"`, "/signup/*"},
		{`body:"login failed"`, "login failed"},
		{`body:"foo\"bar"`, `foo"bar`},
		{`service:"orders"`, "orders"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.input, err)
			}
			got := q.Terms[0]
			if got.Value != tc.wantVal {
				t.Errorf("Value: got %q want %q", got.Value, tc.wantVal)
			}
			if !got.QuotedLiteral {
				t.Errorf("QuotedLiteral: got false want true")
			}
			if got.Wildcard != WildcardNone {
				t.Errorf("Wildcard: got %v want None (quoted values do not wildcard)", got.Wildcard)
			}
		})
	}
}

func TestParse_QuotedMultiTokenPreservesWhitespace(t *testing.T) {
	t.Parallel()

	q, err := Parse(`service:orders body:"login failed" method:POST`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Terms) != 3 {
		t.Fatalf("got %d terms want 3", len(q.Terms))
	}
	if q.Terms[1].Value != "login failed" {
		t.Errorf("body value: got %q want %q", q.Terms[1].Value, "login failed")
	}
}

func TestParse_Negation(t *testing.T) {
	t.Parallel()

	q, err := Parse("-service:foo")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := q.Terms[0]
	if !got.Negated {
		t.Errorf("Negated: got false want true")
	}
	if got.Field != FieldService || got.Value != "foo" {
		t.Errorf("term: got %+v", got)
	}
}

func TestParse_NegationInteractsWithWildcardsAndQuotes(t *testing.T) {
	t.Parallel()

	q, err := Parse(`-host:billing-api* -body:"login failed"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Terms) != 2 {
		t.Fatalf("got %d terms want 2", len(q.Terms))
	}
	h := q.Terms[0]
	if !h.Negated || h.Field != FieldHost || h.Value != "billing-api" || h.Wildcard != WildcardPrefix {
		t.Errorf("host term: got %+v", h)
	}
	b := q.Terms[1]
	if !b.Negated || b.Field != FieldBody || b.Value != "login failed" || !b.QuotedLiteral {
		t.Errorf("body term: got %+v", b)
	}
}

func TestParse_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantToken string
		wantMsg   string
	}{
		{
			name:      "unknown key",
			input:     "foo:bar",
			wantToken: "foo:bar",
			wantMsg:   "unknown key",
		},
		{
			name:      "empty value",
			input:     "service:",
			wantToken: "service:",
			wantMsg:   "empty value",
		},
		{
			name:      "leading colon",
			input:     ":foo",
			wantToken: ":foo",
			wantMsg:   "must start with a key",
		},
		{
			name:      "bad method",
			input:     "method:FETCH",
			wantToken: "method:FETCH",
			wantMsg:   "unknown HTTP method",
		},
		{
			name:      "bad status",
			input:     "status:notastatus",
			wantToken: "status:notastatus",
			wantMsg:   "integer status code",
		},
		{
			name:      "out of range status",
			input:     "status:99",
			wantToken: "status:99",
			wantMsg:   "integer status code",
		},
		{
			name:      "bad status class",
			input:     "status:6xx",
			wantToken: "status:6xx",
			wantMsg:   "integer status code",
		},
		{
			name:      "wildcard on method",
			input:     "method:*POST*",
			wantToken: "method:*POST*",
			wantMsg:   "wildcards are not supported",
		},
		{
			name:      "wildcard on status",
			input:     "status:*200*",
			wantToken: "status:*200*",
			wantMsg:   "wildcards are not supported",
		},
		{
			name:      "wildcard on source_ip",
			input:     "source_ip:10.0.0.*",
			wantToken: "source_ip:10.0.0.*",
			wantMsg:   "wildcards are not supported",
		},
		{
			name:      "wildcard on correlation_id",
			input:     "correlation_id:abc*",
			wantToken: "correlation_id:abc*",
			wantMsg:   "wildcards are not supported",
		},
		{
			name:      "internal wildcard",
			input:     "host:foo*bar",
			wantToken: "host:foo*bar",
			wantMsg:   "start or end",
		},
		{
			name:      "all-wildcards value",
			input:     "host:**",
			wantToken: "host:**",
			wantMsg:   "literal between wildcards",
		},
		{
			name:      "unclosed quote",
			input:     `body:"foo`,
			wantToken: `body:"foo`,
			wantMsg:   "unclosed quote",
		},
		{
			name:      "unclosed quote mid-query",
			input:     `service:orders body:"foo bar`,
			wantToken: `body:"foo bar`,
			wantMsg:   "unclosed quote",
		},
		{
			name:      "multi token surfaces first error",
			input:     "service:orders unknown:foo",
			wantToken: "unknown:foo",
			wantMsg:   "unknown key",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tc.input)
			if err == nil {
				t.Fatalf("Parse(%q): expected error", tc.input)
			}
			pe, ok := err.(*ParseError)
			if !ok {
				t.Fatalf("expected *ParseError, got %T (%v)", err, err)
			}
			if pe.Token != tc.wantToken {
				t.Errorf("Token: got %q want %q", pe.Token, tc.wantToken)
			}
			if !strings.Contains(pe.Message, tc.wantMsg) {
				t.Errorf("Message %q does not contain %q", pe.Message, tc.wantMsg)
			}
		})
	}
}

func TestQuery_IsUnindexedScan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"service:orders", false},
		{"host:billing-api", false},
		{"host:billing-api*", false},
		{"path:/api/*", false},
		{"body:foo", false},
		{"body:*foo*", false},
		{"host:*api*", true},
		{"host:*api", true},
		{"path:*signup", true},
		{"service:*foo*", true},
		{"service:orders host:*api*", true},
		{"service:orders -host:*api*", true},
		{"billing-api", false},
		{"billing-api*", false},
		{"*billing*", true},
		{"*billing", true},
		{"service:orders *billing*", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.input, err)
			}
			if got := q.IsUnindexedScan(); got != tc.want {
				t.Errorf("IsUnindexedScan(%q): got %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestQuery_HasRequestOnlyTerm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  bool
	}{
		{"", false},
		{"service:orders", false},
		{"correlation_id:abc", false},
		{"status:200", false},
		{"path:/api", true},
		{"method:GET", true},
		{"source_ip:10.0.0.1", true},
		{"body:err", true},
		{"host:example.com", true},
		{"service:orders method:GET", true},
		{"-method:GET", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := q.HasRequestOnlyTerm(); got != tc.want {
				t.Errorf("HasRequestOnlyTerm(%q): got %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestParse_FreeformBareToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		wantVal  string
		wantWild Wildcard
		wantNeg  bool
		wantQuot bool
	}{
		{"billing-api", "billing-api", WildcardNone, false, false},
		{"billing-api*", "billing-api", WildcardPrefix, false, false},
		{"*billing-api", "billing-api", WildcardSubstring, false, false},
		{"*billing-api*", "billing-api", WildcardSubstring, false, false},
		{"-billing-api", "billing-api", WildcardNone, true, false},
		{"-*api*", "api", WildcardSubstring, true, false},
		{`"login failed"`, "login failed", WildcardNone, false, true},
		{`-"login failed"`, "login failed", WildcardNone, true, true},
		{"OR", "OR", WildcardNone, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.input, err)
			}
			if len(q.Terms) != 1 {
				t.Fatalf("got %d terms want 1", len(q.Terms))
			}
			got := q.Terms[0]
			if got.Field != "" {
				t.Errorf("Field: got %q want empty (freeform)", got.Field)
			}
			if got.Value != tc.wantVal {
				t.Errorf("Value: got %q want %q", got.Value, tc.wantVal)
			}
			if got.Wildcard != tc.wantWild {
				t.Errorf("Wildcard: got %v want %v", got.Wildcard, tc.wantWild)
			}
			if got.Negated != tc.wantNeg {
				t.Errorf("Negated: got %v want %v", got.Negated, tc.wantNeg)
			}
			if got.QuotedLiteral != tc.wantQuot {
				t.Errorf("QuotedLiteral: got %v want %v", got.QuotedLiteral, tc.wantQuot)
			}
		})
	}
}

func TestParse_FreeformMixedWithFieldQualified(t *testing.T) {
	t.Parallel()

	q, err := Parse("billing-api -path:/health service:billing-svc")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(q.Terms) != 3 {
		t.Fatalf("got %d terms want 3", len(q.Terms))
	}
	if q.Terms[0].Field != "" || q.Terms[0].Value != "billing-api" {
		t.Errorf("term[0]: got %+v want freeform billing-api", q.Terms[0])
	}
	if q.Terms[1].Field != FieldPath || !q.Terms[1].Negated || q.Terms[1].Value != "/health" {
		t.Errorf("term[1]: got %+v want -path:/health", q.Terms[1])
	}
	if q.Terms[2].Field != FieldService || q.Terms[2].Value != "billing-svc" {
		t.Errorf("term[2]: got %+v want service:billing-svc", q.Terms[2])
	}
}

func TestQuery_HasNonTemporalTerm(t *testing.T) {
	t.Parallel()
	q, _ := Parse("")
	if q.HasNonTemporalTerm() {
		t.Error("empty query: HasNonTemporalTerm should be false")
	}
	q, _ = Parse("service:orders")
	if !q.HasNonTemporalTerm() {
		t.Error("service:orders: HasNonTemporalTerm should be true")
	}
}
