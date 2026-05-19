package redactcmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/radarnex/httpcatch/internal/capture"
)

func TestDiffRecords_HeaderChange(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{
		Headers: map[string][]string{"Authorization": {"Bearer x"}},
	}
	after := &capture.CapturedRequest{
		Headers: map[string][]string{"Authorization": {"[REDACTED]"}},
	}
	got := diffRecords(before, after)
	if len(got) != 1 || got[0].Path != "headers.Authorization" {
		t.Fatalf("entries: %+v", got)
	}
	if got[0].Before != `"Bearer x"` || got[0].After != `"[REDACTED]"` {
		t.Errorf("formatting: %+v", got[0])
	}
}

func TestDiffRecords_CaseInsensitiveHeaderKey(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{
		Headers: map[string][]string{"authorization": {"Bearer x"}},
	}
	after := &capture.CapturedRequest{
		Headers: map[string][]string{"authorization": {"[REDACTED]"}},
	}
	got := diffRecords(before, after)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].Path, "headers.") {
		t.Errorf("path: %q", got[0].Path)
	}
}

func TestDiffRecords_HeaderRemoved(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{
		Headers: map[string][]string{"Cookie": {"session=abc"}},
	}
	after := &capture.CapturedRequest{
		Headers: map[string][]string{},
	}
	got := diffRecords(before, after)
	if len(got) != 1 || got[0].After != removedMarker {
		t.Fatalf("entries: %+v", got)
	}
}

func TestDiffRecords_HeaderAdded(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{Headers: map[string][]string{}}
	after := &capture.CapturedRequest{
		Headers: map[string][]string{"X-New": {"v"}},
	}
	got := diffRecords(before, after)
	if len(got) != 1 || got[0].Before != addedMarker {
		t.Fatalf("entries: %+v", got)
	}
}

func TestDiffRecords_QueryChange(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{
		Query: map[string][]string{"token": {"shhh"}},
	}
	after := &capture.CapturedRequest{
		Query: map[string][]string{"token": {"[REDACTED]"}},
	}
	got := diffRecords(before, after)
	if len(got) != 1 || got[0].Path != "query.token" {
		t.Fatalf("entries: %+v", got)
	}
}

func TestDiffRecords_CookieRedacted(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{
		Cookies: []capture.Cookie{{Name: "session", Value: "xyz"}},
	}
	after := &capture.CapturedRequest{
		Cookies: []capture.Cookie{{Name: "session", Value: "[REDACTED]"}},
	}
	got := diffRecords(before, after)
	if len(got) != 1 || got[0].Path != "cookies.session" {
		t.Fatalf("entries: %+v", got)
	}
	if got[0].Before != "xyz" || got[0].After != "[REDACTED]" {
		t.Errorf("formatting: %+v", got[0])
	}
}

func TestDiffRecords_CookieStripped(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{
		Cookies: []capture.Cookie{{Name: "track", Value: "xx"}},
	}
	after := &capture.CapturedRequest{Cookies: []capture.Cookie{}}
	got := diffRecords(before, after)
	if len(got) != 1 || got[0].After != removedMarker {
		t.Fatalf("entries: %+v", got)
	}
}

func TestDiffRecords_BodyDiff(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{Body: []byte(`{"password":"pw"}`)}
	after := &capture.CapturedRequest{Body: []byte(`{"password":"[REDACTED]"}`)}
	got := diffRecords(before, after)
	if len(got) != 1 || got[0].Path != "body" {
		t.Fatalf("entries: %+v", got)
	}
	if !strings.Contains(got[0].Before, "17 bytes") {
		t.Errorf("before: %q", got[0].Before)
	}
	if !strings.Contains(got[0].After, "25 bytes") {
		t.Errorf("after: %q", got[0].After)
	}
}

func TestDiffRecords_PathChange(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{Path: "/old"}
	after := &capture.CapturedRequest{Path: "/new"}
	got := diffRecords(before, after)
	if len(got) != 1 || got[0].Path != "path" {
		t.Fatalf("entries: %+v", got)
	}
}

func TestDiffRecords_NoChanges(t *testing.T) {
	t.Parallel()
	rec := &capture.CapturedRequest{
		Headers: map[string][]string{"A": {"b"}},
		Query:   map[string][]string{"k": {"v"}},
		Body:    []byte("hello"),
	}
	got := diffRecords(rec, rec)
	if len(got) != 0 {
		t.Errorf("want no entries, got %+v", got)
	}
}

func TestRenderDiff_Empty(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := renderDiff(nil, &buf); err != nil {
		t.Fatalf("renderDiff: %v", err)
	}
	if buf.String() != "no changes\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestRenderDiff_Ordering(t *testing.T) {
	t.Parallel()
	entries := []DiffEntry{
		{Path: "body", Before: "1", After: "2"},
		{Path: "cookies.s", Before: "a", After: "b"},
		{Path: "headers.X", Before: `"x"`, After: `"y"`},
	}
	var buf bytes.Buffer
	if err := renderDiff(entries, &buf); err != nil {
		t.Fatalf("renderDiff: %v", err)
	}
	want := "body: 1 -> 2\n" +
		"cookies.s: a -> b\n" +
		"headers.X: \"x\" -> \"y\"\n"
	if buf.String() != want {
		t.Errorf("got %q want %q", buf.String(), want)
	}
}
