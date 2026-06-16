package redactcmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/radarnex/httpcatch/internal/capture"
)

func TestFormatSliceSide_MaskBefore_SingleValue(t *testing.T) {
	t.Parallel()
	got := formatSliceSide([]string{"Bearer secrettoken"}, true, addedMarker, true)
	want := "<masked: 18 chars>"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFormatSliceSide_MaskBefore_MultiValue(t *testing.T) {
	t.Parallel()
	got := formatSliceSide([]string{"Bearer secrettoken", "extra"}, true, addedMarker, true)
	want := "<masked: 18 chars>, <masked: 5 chars>"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFormatSliceSide_MaskBefore_EmptyValue(t *testing.T) {
	t.Parallel()
	// A present-but-empty-string value in a slice renders as its quoted form,
	// not a mask wrapper, because there is nothing to obscure.
	got := formatSliceSide([]string{""}, true, addedMarker, true)
	want := "<masked: 0 chars>"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFormatSliceSide_MaskBefore_AbsentKey(t *testing.T) {
	t.Parallel()
	// An absent key always returns the sentinel regardless of mask.
	got := formatSliceSide(nil, false, addedMarker, true)
	if got != addedMarker {
		t.Errorf("got %q want %q", got, addedMarker)
	}
}

func TestFormatSliceSide_NoMask(t *testing.T) {
	t.Parallel()
	got := formatSliceSide([]string{"Bearer secrettoken", "extra"}, true, addedMarker, false)
	want := `"Bearer secrettoken", "extra"`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestFormatSliceSide_NoMask_EmptySlice(t *testing.T) {
	t.Parallel()
	got := formatSliceSide([]string{}, true, addedMarker, false)
	if got != `""` {
		t.Errorf("got %q want empty-string sentinel", got)
	}
}

func TestDiffCookieSlice_MaskBefore(t *testing.T) {
	t.Parallel()
	before := []capture.Cookie{{Name: "session", Value: "s3cr3t"}}
	after := []capture.Cookie{{Name: "session", Value: "[REDACTED]"}}
	got := diffCookieSlice(before, after, true)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(got), got)
	}
	if got[0].Before != "<masked: 6 chars>" {
		t.Errorf("before: got %q want <masked: 6 chars>", got[0].Before)
	}
	if got[0].After != "[REDACTED]" {
		t.Errorf("after: got %q want [REDACTED]", got[0].After)
	}
}

func TestDiffCookieSlice_NoMask(t *testing.T) {
	t.Parallel()
	before := []capture.Cookie{{Name: "session", Value: "s3cr3t"}}
	after := []capture.Cookie{{Name: "session", Value: "[REDACTED]"}}
	got := diffCookieSlice(before, after, false)
	if len(got) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(got), got)
	}
	if got[0].Before != "s3cr3t" {
		t.Errorf("before: got %q want s3cr3t", got[0].Before)
	}
}

func TestDiffRecords_MaskBefore_HeadersCookiesQuery(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{
		Headers: map[string][]string{"Authorization": {"Bearer realtoken"}},
		Query:   map[string][]string{"token": {"shhh"}},
		Cookies: []capture.Cookie{{Name: "session", Value: "s3cr3t"}},
	}
	after := &capture.CapturedRequest{
		Headers: map[string][]string{"Authorization": {"[REDACTED]"}},
		Query:   map[string][]string{"token": {"[REDACTED]"}},
		Cookies: []capture.Cookie{{Name: "session", Value: "[REDACTED]"}},
	}
	got := diffRecords(before, after, true)
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(got), got)
	}
	for _, e := range got {
		if strings.Contains(e.Before, "realtoken") || strings.Contains(e.Before, "shhh") || strings.Contains(e.Before, "s3cr3t") {
			t.Errorf("before-side leaks cleartext in entry %+v", e)
		}
		if !strings.Contains(e.Before, "<masked:") {
			t.Errorf("before-side missing mask sentinel in entry %+v", e)
		}
		if !strings.Contains(e.After, "[REDACTED]") {
			t.Errorf("after-side should be cleartext redacted value in entry %+v", e)
		}
	}
}

func TestDiffRecords_HeaderChange(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{
		Headers: map[string][]string{"Authorization": {"Bearer x"}},
	}
	after := &capture.CapturedRequest{
		Headers: map[string][]string{"Authorization": {"[REDACTED]"}},
	}
	got := diffRecords(before, after, false)
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
	got := diffRecords(before, after, false)
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
	got := diffRecords(before, after, false)
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
	got := diffRecords(before, after, false)
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
	got := diffRecords(before, after, false)
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
	got := diffRecords(before, after, false)
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
	got := diffRecords(before, after, false)
	if len(got) != 1 || got[0].After != removedMarker {
		t.Fatalf("entries: %+v", got)
	}
}

func TestDiffRecords_BodyDiff(t *testing.T) {
	t.Parallel()
	before := &capture.CapturedRequest{Body: []byte(`{"password":"pw"}`)}
	after := &capture.CapturedRequest{Body: []byte(`{"password":"[REDACTED]"}`)}
	got := diffRecords(before, after, false)
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
	got := diffRecords(before, after, false)
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
	got := diffRecords(rec, rec, false)
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
