package redactcmd

import (
	"bytes"
	"fmt"
	"io"
	"net/textproto"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/radarnex/httpcatch/internal/capture"
)

// DiffEntry is one rendered line of the before/after diff. Path is the sort
// key (e.g. "headers.Authorization", "body"); Before / After are the already-
// formatted strings (with `<removed>` / `<added>` sentinels where appropriate).
type DiffEntry struct {
	Path   string
	Before string
	After  string
}

const (
	removedMarker = "<removed>"
	addedMarker   = "<added>"
	arrow         = " -> "
)

// diffRecords compares two records field-by-field and returns one entry per
// modified field. Pure function: no I/O. Output is sorted by Path so callers
// can rely on byte-stable rendering.
//
// When maskBefore is true, header, query-parameter, and cookie values on the
// before-side are replaced with "<masked: N chars>" rather than rendered in
// cleartext. The after-side is always rendered cleartext so that redaction
// rule effects remain visible. Body is always summarised as "%d bytes" on
// both sides regardless of maskBefore.
func diffRecords(before, after *capture.CapturedRequest, maskBefore bool) []DiffEntry {
	var entries []DiffEntry

	if before.Path != after.Path {
		entries = append(entries, DiffEntry{
			Path:   "path",
			Before: before.Path,
			After:  after.Path,
		})
	}

	entries = append(entries, diffStringSliceMap("headers", before.Headers, after.Headers, canonicalHeaderKey, maskBefore)...)
	entries = append(entries, diffStringSliceMap("query", before.Query, after.Query, identityKey, maskBefore)...)
	entries = append(entries, diffCookieSlice(before.Cookies, after.Cookies, maskBefore)...)

	if !bytes.Equal(before.Body, after.Body) {
		entries = append(entries, DiffEntry{
			Path:   "body",
			Before: fmt.Sprintf("%d bytes", len(before.Body)),
			After:  fmt.Sprintf("%d bytes", len(after.Body)),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return entries
}

func renderDiff(entries []DiffEntry, w io.Writer) error {
	if len(entries) == 0 {
		_, err := io.WriteString(w, "no changes\n")
		return err
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Path)
		b.WriteString(": ")
		b.WriteString(e.Before)
		b.WriteString(arrow)
		b.WriteString(e.After)
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func canonicalHeaderKey(k string) string { return textproto.CanonicalMIMEHeaderKey(k) }
func identityKey(k string) string        { return k }

// diffStringSliceMap walks the union of keys in `before` and `after` and emits
// one entry per key whose values differ. normaliseKey collapses case-variant
// keys (e.g. canonicalising header names) so an entry like
// "Authorization" -> "authorization" does not show up as one add and one
// remove. The original display key is preserved for the output line.
func diffStringSliceMap(section string, before, after map[string][]string, normaliseKey func(string) string, maskBefore bool) []DiffEntry {
	type side struct {
		display string
		vals    []string
		present bool
	}
	type pair struct {
		left  side
		right side
	}
	merged := make(map[string]*pair)
	for k, v := range before {
		nk := normaliseKey(k)
		merged[nk] = &pair{left: side{display: k, vals: v, present: true}}
	}
	for k, v := range after {
		nk := normaliseKey(k)
		if p, ok := merged[nk]; ok {
			p.right = side{display: k, vals: v, present: true}
		} else {
			merged[nk] = &pair{right: side{display: k, vals: v, present: true}}
		}
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var entries []DiffEntry
	for _, nk := range keys {
		p := merged[nk]
		if p.left.present && p.right.present && stringSliceEqual(p.left.vals, p.right.vals) {
			continue
		}
		display := p.left.display
		if display == "" {
			display = p.right.display
		}
		entries = append(entries, DiffEntry{
			Path:   section + "." + display,
			Before: formatSliceSide(p.left.vals, p.left.present, addedMarker, maskBefore),
			After:  formatSliceSide(p.right.vals, p.right.present, removedMarker, false),
		})
	}
	return entries
}

// formatSliceSide renders one side of a diff entry. An absent key uses the
// caller-supplied sentinel (`<removed>` for the before-side, `<added>` for
// the after-side); a present-but-empty slice renders as `""`; otherwise the
// values are quoted and joined with `, `.
//
// When mask is true each value is rendered as `<masked: N chars>` (where N is
// the UTF-8 character count) rather than the cleartext quoted value. Empty
// values and absent keys are unaffected by mask.
func formatSliceSide(vals []string, present bool, absentMarker string, mask bool) string {
	if !present {
		return absentMarker
	}
	if len(vals) == 0 {
		return `""`
	}
	parts := make([]string, len(vals))
	for i, v := range vals {
		if mask {
			parts[i] = fmt.Sprintf("<masked: %d chars>", utf8.RuneCountInString(v))
		} else {
			parts[i] = fmt.Sprintf("%q", v)
		}
	}
	return strings.Join(parts, ", ")
}

func diffCookieSlice(before, after []capture.Cookie, maskBefore bool) []DiffEntry {
	beforeMap := make(map[string]string, len(before))
	afterMap := make(map[string]string, len(after))
	for _, c := range before {
		beforeMap[c.Name] = c.Value
	}
	for _, c := range after {
		afterMap[c.Name] = c.Value
	}
	names := make(map[string]struct{}, len(beforeMap)+len(afterMap))
	for n := range beforeMap {
		names[n] = struct{}{}
	}
	for n := range afterMap {
		names[n] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	var entries []DiffEntry
	for _, n := range sorted {
		bv, bok := beforeMap[n]
		av, aok := afterMap[n]
		if bok && aok && bv == av {
			continue
		}
		beforeStr := addedMarker
		afterStr := removedMarker
		if bok {
			if maskBefore {
				beforeStr = fmt.Sprintf("<masked: %d chars>", utf8.RuneCountInString(bv))
			} else {
				beforeStr = bv
			}
		}
		if aok {
			afterStr = av
		}
		entries = append(entries, DiffEntry{
			Path:   "cookies." + n,
			Before: beforeStr,
			After:  afterStr,
		})
	}
	return entries
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
