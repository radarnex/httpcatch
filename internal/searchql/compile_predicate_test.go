package searchql

import (
	"testing"

	"github.com/radarnex/httpcatch/internal/capture"
)

func fixture() *capture.CapturedRequest {
	return &capture.CapturedRequest{
		ID:            "r1",
		Service:       "orders",
		Method:        "POST",
		Path:          "/api/orders/1",
		SourceIP:      "10.0.0.1",
		CorrelationID: "abc-123",
		Headers:       map[string][]string{"Host": {"billing-api.svc.local"}},
		Body:          []byte(`{"error":"login failed"}`),
	}
}

func TestPredicate_PerField(t *testing.T) {
	t.Parallel()

	r := fixture()
	tests := []struct {
		input string
		want  bool
	}{
		{"service:orders", true},
		{"service:payments", false},
		{"host:billing-api.svc.local", true},
		{"host:other.com", false},
		{"method:POST", true},
		{"method:get", false},
		{"path:/api/orders/1", true},
		{"path:/api/orders", false},
		{"source_ip:10.0.0.1", true},
		{"source_ip:10.0.0.2", false},
		{"correlation_id:abc-123", true},
		{"correlation_id:other", false},
		{"body:error", true},
		{"body:missing", false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			p := CompilePredicate(q)
			if got := p(r); got != tc.want {
				t.Errorf("predicate(%q): got %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPredicate_Wildcards(t *testing.T) {
	t.Parallel()

	r := fixture()
	tests := []struct {
		input string
		want  bool
	}{
		{"host:billing-api*", true},
		{"host:billing-other*", false},
		{"host:*api*", true},
		{"host:*nope*", false},
		{"host:*svc.local", true},
		{"path:/api/orders*", true},
		{"path:/api/users*", false},
		{"path:*orders*", true},
		{"service:ord*", true},
		{"service:*orders*", true},
		{"body:foo*", false},
		{"body:*foo*", false},
		{"body:*login*", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			p := CompilePredicate(q)
			if got := p(r); got != tc.want {
				t.Errorf("predicate(%q): got %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPredicate_Quoting(t *testing.T) {
	t.Parallel()

	r := fixture()
	tests := []struct {
		input string
		want  bool
	}{
		{`host:"billing-api.svc.local"`, true},
		{`host:"billing-api*"`, false}, // quoted: literal *, no match
		{`body:"login failed"`, true},
		{`body:"missing phrase"`, false},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			p := CompilePredicate(q)
			if got := p(r); got != tc.want {
				t.Errorf("predicate(%q): got %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPredicate_Negation(t *testing.T) {
	t.Parallel()

	r := fixture()
	tests := []struct {
		input string
		want  bool
	}{
		{"-service:payments", true},
		{"-service:orders", false},
		{"-host:billing-api*", false},
		{"-host:billing-other*", true},
		{`-body:"login failed"`, false},
		{`-body:"missing"`, true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			p := CompilePredicate(q)
			if got := p(r); got != tc.want {
				t.Errorf("predicate(%q): got %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPredicate_MultiTermAND(t *testing.T) {
	t.Parallel()

	r := fixture()
	q, _ := Parse("service:orders method:POST")
	if !CompilePredicate(q)(r) {
		t.Error("orders+POST should match")
	}

	q, _ = Parse("service:orders method:GET")
	if CompilePredicate(q)(r) {
		t.Error("orders+GET should not match")
	}

	q, _ = Parse("service:orders -method:GET host:billing-api*")
	if !CompilePredicate(q)(r) {
		t.Error("orders+-GET+billing-api* should match")
	}
}

func TestPredicate_EmptyQuery_MatchesAll(t *testing.T) {
	t.Parallel()
	r := fixture()
	if !CompilePredicate(Query{})(r) {
		t.Error("empty query should match every record")
	}
}

func TestPredicate_PathExactByDefault(t *testing.T) {
	t.Parallel()
	r := fixture()
	// Bare path is exact match (a wildcard is required for prefix).
	q, _ := Parse("path:/api/orders")
	if CompilePredicate(q)(r) {
		t.Error("bare path:/api/orders should not match /api/orders/1 — bare is exact")
	}
	q, _ = Parse("path:/api/orders*")
	if !CompilePredicate(q)(r) {
		t.Error("path:/api/orders* should match /api/orders/1 via prefix")
	}
}

func TestPredicate_BodyIsSubstring(t *testing.T) {
	t.Parallel()
	r := fixture()
	q, _ := Parse("body:error")
	if !CompilePredicate(q)(r) {
		t.Error("body substring should match")
	}
}

// headersFixture is a CapturedRequest with a representative header set used by
// the predicate header tests: single-value User-Agent, multi-value
// X-Forwarded-For (mimicking what http.Header.Add produces), no X-Trace-Id.
func headersFixture() *capture.CapturedRequest {
	r := fixture()
	r.Headers = map[string][]string{
		"Host":            {"billing-api.svc.local"},
		"User-Agent":      {"client/0.3 (probe)"},
		"X-Forwarded-For": {"10.0.0.1", "10.0.0.2"},
		"Content-Type":    {"application/json"},
	}
	return r
}

func TestPredicate_HeadersKeyword_MatchesNameOrValue(t *testing.T) {
	t.Parallel()
	r := headersFixture()

	tests := []struct {
		input string
		want  bool
	}{
		{"headers:User-Agent", true}, // matches a header name
		{"headers:client/0.3", true}, // matches a single-value header value
		{"headers:10.0.0.2", true},   // matches the 2nd value of a multi-value header
		{"headers:application/json", true},
		{"headers:absent", false},
		{"headers:*client*", true}, // scanned dim: wildcards collapse
		{"-headers:absent", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := CompilePredicate(q)(r); got != tc.want {
				t.Errorf("predicate(%q): got %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPredicate_HeaderNamed(t *testing.T) {
	t.Parallel()
	r := headersFixture()

	tests := []struct {
		input string
		want  bool
	}{
		{"header.user-agent:client", true},
		{"header.User-Agent:client", true},
		{"header.USER-AGENT:0.3", true},
		{"header.User-Agent:nope", false},
		{"header.x-forwarded-for:10.0.0.1", true}, // 1st value
		{"header.x-forwarded-for:10.0.0.2", true}, // 2nd value
		{"header.x-forwarded-for:10.0.0.9", false},
		{"header.x-trace-id:abc", false}, // missing header never matches positive
		{"-header.x-trace-id:abc", true}, // negation of missing matches
		{"-header.user-agent:client", false},
		{"-header.user-agent:nope", true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := CompilePredicate(q)(r); got != tc.want {
				t.Errorf("predicate(%q): got %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPredicate_HeaderNamed_QuotedPhrase(t *testing.T) {
	t.Parallel()
	r := headersFixture()
	r.Headers["User-Agent"] = []string{"Mozilla/5.0 (Macintosh) AppleWebKit/537"}

	q, err := Parse(`header.user-agent:"Mozilla/5.0 (Macintosh)"`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !CompilePredicate(q)(r) {
		t.Error("quoted phrase should substring-match")
	}
}

func TestPredicate_Freeform(t *testing.T) {
	t.Parallel()
	r := headersFixture()

	tests := []struct {
		input string
		want  bool
	}{
		{"orders", true},                // matches service
		{"billing-api.svc.local", true}, // matches host
		{"/api/orders/1", true},         // matches path exactly
		{"login", true},                 // matches body substring ({"error":"login failed"})
		{"client/0.3", true},            // matches header value
		{"User-Agent", true},            // matches header name
		{"absent", false},               // matches nothing
		{"POST", false},                 // method is not in Tier-1
		{"abc-123", false},              // correlation_id is not in Tier-1
		{"10.0.0.1", false},             // source_ip is not Tier-1 (note: 10.0.0.1 also in X-Forwarded-For header, which IS Tier-1)
	}
	// 10.0.0.1 is also in X-Forwarded-For header (a Tier-1 field), so it should match.
	tests[len(tests)-1].want = true

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := CompilePredicate(q)(r); got != tc.want {
				t.Errorf("predicate(%q): got %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPredicate_Freeform_Wildcards(t *testing.T) {
	t.Parallel()
	r := headersFixture()

	tests := []struct {
		input string
		want  bool
	}{
		{"billing-api*", true}, // prefix on host
		{"order*", true},       // prefix on service
		{"*orders*", true},     // substring on service and path
		{"*nope*", false},
		{`"login failed"`, true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			q, err := Parse(tc.input)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := CompilePredicate(q)(r); got != tc.want {
				t.Errorf("predicate(%q): got %v want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestPredicate_Freeform_Negated(t *testing.T) {
	t.Parallel()
	r := headersFixture()

	q, _ := Parse("-billing-api.svc.local")
	if CompilePredicate(q)(r) {
		t.Error("negation of matching freeform should reject")
	}

	q, _ = Parse("-absent")
	if !CompilePredicate(q)(r) {
		t.Error("negation of non-matching freeform should accept")
	}
}

func TestPredicate_Freeform_MultiTokenAND(t *testing.T) {
	t.Parallel()
	r := headersFixture()

	// "orders" matches service; "billing-api" matches host. Both required.
	q, _ := Parse("orders billing-api")
	if !CompilePredicate(q)(r) {
		t.Error("orders+billing-api should match")
	}

	// "orders" matches but "missing" does not.
	q, _ = Parse("orders missing")
	if CompilePredicate(q)(r) {
		t.Error("orders+missing should not match")
	}
}

func TestPredicate_Freeform_MixedWithFieldQualified(t *testing.T) {
	t.Parallel()
	r := headersFixture()

	q, _ := Parse("service:orders billing-api")
	if !CompilePredicate(q)(r) {
		t.Error("service:orders + billing-api should match")
	}

	q, _ = Parse("service:payments billing-api")
	if CompilePredicate(q)(r) {
		t.Error("service:payments + billing-api should not match (different service)")
	}
}

func TestPredicate_HeadersAndHeaderCompose(t *testing.T) {
	t.Parallel()
	r := headersFixture()

	q, _ := Parse("service:orders -header.user-agent:bot headers:0.3")
	if !CompilePredicate(q)(r) {
		t.Error("composed query should match")
	}

	q, _ = Parse("service:orders header.user-agent:bot")
	if CompilePredicate(q)(r) {
		t.Error("non-matching header should reject")
	}
}
