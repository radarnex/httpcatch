// Package searchql parses the httpcatch search-language and compiles parsed
// queries into SQL fragments or in-memory predicates. The parser, AST, and
// compilers share a single Query type so the SQLite and memory backends behave
// identically.
//
// The accepted grammar is whitespace-separated tokens AND'd together. A token
// is either a key:value pair naming one of the known keys (host, path,
// service, body, method, status, source_ip, correlation_id, headers,
// header.<name>) or a bare freeform value that matches the term against a
// fixed union of fields ("Tier-1": host, path, service, all body and headers
// columns across both tables, plus correlated event request/response paths
// and bodies). Tokens may carry a leading '-' for negation, may quote values
// with double-quotes to preserve whitespace and treat '*' as literal, and may
// use '*' wildcards at the start, end, or both ends of an unquoted value.
package searchql

import (
	"fmt"
	"net/textproto"
	"strconv"
	"strings"
)

// Field is one of the recognised key names. The empty Field marks a freeform
// term whose value is matched against the Tier-1 union of fields.
type Field string

const (
	FieldHost          Field = "host"
	FieldPath          Field = "path"
	FieldService       Field = "service"
	FieldBody          Field = "body"
	FieldHeaders       Field = "headers"
	FieldHeader        Field = "header"
	FieldMethod        Field = "method"
	FieldStatus        Field = "status"
	FieldSourceIP      Field = "source_ip"
	FieldCorrelationID Field = "correlation_id"
)

// knownFields lists every key accepted by Parse. The `header.<name>:` form is
// not in this table — the parser detects the `header.` prefix separately so it
// can canonicalise the name segment and reject wildcards there.
var knownFields = map[string]Field{
	string(FieldHost):          FieldHost,
	string(FieldPath):          FieldPath,
	string(FieldService):       FieldService,
	string(FieldBody):          FieldBody,
	string(FieldHeaders):       FieldHeaders,
	string(FieldMethod):        FieldMethod,
	string(FieldStatus):        FieldStatus,
	string(FieldSourceIP):      FieldSourceIP,
	string(FieldCorrelationID): FieldCorrelationID,
}

// indexedFields are the fields whose default match is exact and whose leading
// wildcard forces a substring scan that bypasses the column index.
var indexedFields = map[Field]struct{}{
	FieldHost:    {},
	FieldPath:    {},
	FieldService: {},
}

// structuredFields are exact-match-only fields. Wildcards on these return a
// parse error.
var structuredFields = map[Field]struct{}{
	FieldMethod:        {},
	FieldStatus:        {},
	FieldSourceIP:      {},
	FieldCorrelationID: {},
}

// Wildcard classifies the wildcard shape of a term's value. Parse assigns it
// purely on syntax: a trailing-only '*' is WildcardPrefix; any leading '*'
// (with or without trailing) collapses to WildcardSubstring. Field semantics
// decide whether the wildcard actually changes the emitted predicate.
type Wildcard int

const (
	WildcardNone Wildcard = iota
	WildcardPrefix
	WildcardSubstring
)

// Term is one AND-leaf of a Query. Field is the parsed key; Value is the
// post-canonicalisation value with wildcard markers and surrounding quotes
// stripped. Wildcard is set from the value's syntactic shape; QuotedLiteral
// records whether the operator wrapped the value in double quotes (so '*'
// is literal); Negated is set when the token had a leading '-'.
type Term struct {
	Field         Field
	Value         string
	Wildcard      Wildcard
	QuotedLiteral bool
	Negated       bool

	// HeaderName is the canonical MIME header name (per
	// textproto.CanonicalMIMEHeaderKey) of a `header.<name>:` term. It is set
	// only when Field == FieldHeader.
	HeaderName string

	// StatusFilter is populated by Parse when Field == FieldStatus so both
	// compilers can dispatch on the same parsed form (exact vs class) without
	// re-parsing the value string.
	StatusFilter *StatusFilter
}

// StatusFilter holds a parsed status filter — either an exact code or a class
// (e.g. "2xx"). Exactly one of Exact and Class is set.
type StatusFilter struct {
	Exact int
	Class string
}

// ClassRange returns the inclusive [lo, hi] range covered by a class filter
// (e.g. "5xx" → 500, 599). Callers must only invoke this when Class is set.
func (sf *StatusFilter) ClassRange() (lo, hi int) {
	lo = int(sf.Class[0]-'0') * 100
	return lo, lo + 99
}

// Query is a flat list of terms AND'd together. An empty Query matches every
// record (no field constraint).
type Query struct {
	Terms []Term
}

// IsUnindexedScan reports whether any term forces an indexed dimension into
// substring matching — a leading wildcard against host, path, or service, or
// any leading-wildcard freeform term whose union expansion drives the indexed
// arms into substring matching. The caller uses this to surface the
// cost-class warning.
func (q Query) IsUnindexedScan() bool {
	for _, t := range q.Terms {
		if t.Wildcard != WildcardSubstring {
			continue
		}
		if t.Field == "" {
			return true
		}
		if _, ok := indexedFields[t.Field]; ok {
			return true
		}
	}
	return false
}

// ParseError carries the offending token text alongside the explanation. The
// admin layer reports the field as "q" and the message via Error().
type ParseError struct {
	Token   string
	Message string
}

func (e *ParseError) Error() string {
	if e.Token == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Token, e.Message)
}

// Parse tokenises s (quote-aware, whitespace-separated) and produces a Query
// of AND'd terms. The grammar accepts only key:value tokens for the documented
// keys; everything else is rejected with a ParseError naming the offending
// token.
func Parse(s string) (Query, error) {
	tokens, err := tokenize(s)
	if err != nil {
		return Query{}, err
	}
	if len(tokens) == 0 {
		return Query{}, nil
	}

	terms := make([]Term, 0, len(tokens))
	for _, tok := range tokens {
		t, err := parseToken(tok)
		if err != nil {
			return Query{}, err
		}
		terms = append(terms, t)
	}
	return Query{Terms: terms}, nil
}

// tokenize walks s and emits one token per whitespace-separated run, treating
// the body of a double-quoted region as a single unit so internal whitespace
// survives the split. Escaped quotes (\") inside a quoted value are not
// treated as terminators. An unterminated quote returns a ParseError whose
// token is the remainder of the input from the opening quote.
func tokenize(s string) ([]string, error) {
	var tokens []string
	i := 0
	n := len(s)
	for i < n {
		for i < n && isASCIISpace(s[i]) {
			i++
		}
		if i >= n {
			break
		}
		start := i
		inQuote := false
		for i < n {
			c := s[i]
			if inQuote {
				if c == '\\' && i+1 < n && s[i+1] == '"' {
					i += 2
					continue
				}
				if c == '"' {
					inQuote = false
					i++
					continue
				}
				i++
				continue
			}
			if c == '"' {
				inQuote = true
				i++
				continue
			}
			if isASCIISpace(c) {
				break
			}
			i++
		}
		if inQuote {
			return nil, &ParseError{Token: s[start:], Message: "unclosed quote"}
		}
		tokens = append(tokens, s[start:i])
	}
	return tokens, nil
}

// isASCIISpace mirrors strings.Fields' notion of whitespace for our token
// stream — space, tab, newline, carriage-return, vertical-tab, form-feed.
func isASCIISpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	}
	return false
}

func parseToken(raw string) (Term, error) {
	tok := raw
	negated := false
	if strings.HasPrefix(tok, "-") {
		negated = true
		tok = tok[1:]
		if tok == "" {
			return Term{}, &ParseError{Token: raw, Message: "bare '-' is not a valid token"}
		}
	}

	// A token that begins with `"` is a fully quoted literal and never carries
	// a key prefix — a `:` inside the quotes is data, not the key separator.
	if strings.HasPrefix(tok, "\"") {
		value, wildcard, quoted, err := parseValue(tok)
		if err != nil {
			return Term{}, &ParseError{Token: raw, Message: err.Error()}
		}
		return Term{
			Value:         value,
			Wildcard:      wildcard,
			QuotedLiteral: quoted,
			Negated:       negated,
		}, nil
	}

	idx := strings.IndexByte(tok, ':')
	if idx < 0 {
		value, wildcard, quoted, err := parseValue(tok)
		if err != nil {
			return Term{}, &ParseError{Token: raw, Message: err.Error()}
		}
		return Term{
			Value:         value,
			Wildcard:      wildcard,
			QuotedLiteral: quoted,
			Negated:       negated,
		}, nil
	}
	if idx == 0 {
		return Term{}, &ParseError{Token: raw, Message: "token must start with a key before ':'"}
	}
	key := tok[:idx]
	valueRaw := tok[idx+1:]
	if valueRaw == "" {
		return Term{}, &ParseError{Token: raw, Message: "empty value after ':'"}
	}

	keyLow := strings.ToLower(key)
	field, ok := knownFields[keyLow]
	if !ok {
		if strings.HasPrefix(keyLow, "header.") {
			return parseHeaderTerm(raw, key, valueRaw, negated)
		}
		return Term{}, &ParseError{Token: raw, Message: fmt.Sprintf("unknown key %q", key)}
	}

	value, wildcard, quoted, err := parseValue(valueRaw)
	if err != nil {
		return Term{}, &ParseError{Token: raw, Message: err.Error()}
	}

	if _, isStructured := structuredFields[field]; isStructured && wildcard != WildcardNone {
		return Term{}, &ParseError{Token: raw, Message: fmt.Sprintf("wildcards are not supported on field %q", key)}
	}

	term := Term{
		Field:         field,
		Value:         value,
		Wildcard:      wildcard,
		QuotedLiteral: quoted,
		Negated:       negated,
	}

	switch field {
	case FieldMethod:
		upper := strings.ToUpper(value)
		if _, ok := canonicalHTTPMethods[upper]; !ok {
			return Term{}, &ParseError{Token: raw, Message: fmt.Sprintf("unknown HTTP method %q", value)}
		}
		term.Value = upper

	case FieldStatus:
		sf, err := parseStatusValue(value)
		if err != nil {
			return Term{}, &ParseError{Token: raw, Message: err.Error()}
		}
		term.StatusFilter = sf
	}

	return term, nil
}

// parseHeaderTerm handles `header.<name>:value` tokens. The name segment is
// canonicalised via textproto.CanonicalMIMEHeaderKey so equivalent operator
// spellings (User-Agent / user-agent / USER-AGENT) collapse into one AST. A
// wildcard in the name segment is rejected — fuzzy header-name search is the
// `headers:` keyword's job.
func parseHeaderTerm(raw, key, valueRaw string, negated bool) (Term, error) {
	name := key[len("header."):]
	if name == "" {
		return Term{}, &ParseError{Token: raw, Message: "empty header name after 'header.'"}
	}
	if strings.ContainsRune(name, '*') {
		return Term{}, &ParseError{Token: raw, Message: "wildcards are not supported in header name; use 'headers:' for fuzzy header search"}
	}
	value, wildcard, quoted, err := parseValue(valueRaw)
	if err != nil {
		return Term{}, &ParseError{Token: raw, Message: err.Error()}
	}
	return Term{
		Field:         FieldHeader,
		HeaderName:    textproto.CanonicalMIMEHeaderKey(name),
		Value:         value,
		Wildcard:      wildcard,
		QuotedLiteral: quoted,
		Negated:       negated,
	}, nil
}

// parseValue interprets the value portion of a token. Quoted values strip the
// outer double quotes and un-escape \" to "; their wildcard is always
// WildcardNone. Unquoted values may carry leading and/or trailing '*' — any
// leading '*' (with or without trailing) collapses to WildcardSubstring; a
// trailing-only '*' is WildcardPrefix; otherwise WildcardNone. An interior
// '*' or a value consisting entirely of wildcards is a parse error.
func parseValue(valueRaw string) (value string, wildcard Wildcard, quoted bool, err error) {
	if strings.HasPrefix(valueRaw, "\"") {
		if len(valueRaw) < 2 || !strings.HasSuffix(valueRaw, "\"") {
			return "", 0, false, fmt.Errorf("unclosed quote")
		}
		inner := valueRaw[1 : len(valueRaw)-1]
		value = strings.ReplaceAll(inner, "\\\"", "\"")
		return value, WildcardNone, true, nil
	}

	v := valueRaw
	leading := strings.HasPrefix(v, "*")
	if leading {
		v = v[1:]
	}
	trailing := strings.HasSuffix(v, "*") && len(v) > 0
	if trailing {
		v = v[:len(v)-1]
	}
	if v == "" {
		return "", 0, false, fmt.Errorf("value must contain a literal between wildcards")
	}
	if strings.Contains(v, "*") {
		return "", 0, false, fmt.Errorf("wildcards '*' are only allowed at the start or end of a value")
	}
	switch {
	case leading:
		return v, WildcardSubstring, false, nil
	case trailing:
		return v, WildcardPrefix, false, nil
	default:
		return v, WildcardNone, false, nil
	}
}

// canonicalHTTPMethods is the set of valid HTTP method names. Method values
// are stored uppercase; the parser normalises the input before validating.
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

// parseStatusValue accepts either an exact integer in [100, 599] or a class
// string of the form "Nxx" with N in [1, 5].
func parseStatusValue(s string) (*StatusFilter, error) {
	if len(s) == 3 && s[1] == 'x' && s[2] == 'x' {
		d := s[0]
		if d >= '1' && d <= '5' {
			return &StatusFilter{Class: s}, nil
		}
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 100 || v > 599 {
		return nil, fmt.Errorf("must be an integer status code (e.g. 200) or class form (e.g. 2xx, 5xx)")
	}
	return &StatusFilter{Exact: v}, nil
}
