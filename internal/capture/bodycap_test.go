package capture

import (
	"bytes"
	"fmt"
	"testing"
)

func TestCapBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         []byte
		capBytes     int
		wantBody     []byte
		wantOriginal int
		wantTrunc    bool
	}{
		{
			name:         "under cap",
			body:         []byte("hello"),
			capBytes:     10,
			wantBody:     []byte("hello"),
			wantOriginal: 5,
			wantTrunc:    false,
		},
		{
			name:         "exactly at cap",
			body:         []byte("hello"),
			capBytes:     5,
			wantBody:     []byte("hello"),
			wantOriginal: 5,
			wantTrunc:    false,
		},
		{
			name:         "one byte over cap",
			body:         []byte("hello!"),
			capBytes:     5,
			wantBody:     []byte("hello"),
			wantOriginal: 6, // sentinel: capBytes+1; exact wire size unknown
			wantTrunc:    true,
		},
		{
			name:         "many bytes over cap",
			body:         []byte("hello world"),
			capBytes:     5,
			wantBody:     []byte("hello"),
			wantOriginal: 6, // sentinel: capBytes+1; exact wire size unknown
			wantTrunc:    true,
		},
		{
			name:         "cap zero disables truncation for large body",
			body:         bytes.Repeat([]byte("a"), 10_000),
			capBytes:     0,
			wantBody:     bytes.Repeat([]byte("a"), 10_000),
			wantOriginal: 10_000,
			wantTrunc:    false,
		},
		{
			name:         "cap zero disables truncation for empty body",
			body:         []byte{},
			capBytes:     0,
			wantBody:     []byte{},
			wantOriginal: 0,
			wantTrunc:    false,
		},
		{
			name:         "negative cap disables truncation",
			body:         []byte("xyz"),
			capBytes:     -1,
			wantBody:     []byte("xyz"),
			wantOriginal: 3,
			wantTrunc:    false,
		},
		{
			name:         "empty body under positive cap",
			body:         []byte{},
			capBytes:     16,
			wantBody:     []byte{},
			wantOriginal: 0,
			wantTrunc:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, gotOriginal, gotTrunc, err := CapBody(bytes.NewReader(tt.body), tt.capBytes)
			if err != nil {
				t.Fatalf("CapBody: %v", err)
			}
			if !bytes.Equal(got, tt.wantBody) {
				t.Errorf("body: got %q want %q", got, tt.wantBody)
			}
			if gotOriginal != tt.wantOriginal {
				t.Errorf("originalSize: got %d want %d", gotOriginal, tt.wantOriginal)
			}
			if gotTrunc != tt.wantTrunc {
				t.Errorf("truncated: got %v want %v", gotTrunc, tt.wantTrunc)
			}
		})
	}
}

// TestCapBody_OversizeReportsSentinelSize verifies that bytes past the cap do
// not enter the returned slice and that originalSize is the sentinel value
// (capBytes+1), not the full wire length.
func TestCapBody_OversizeReportsSentinelSize(t *testing.T) {
	t.Parallel()

	const cap = 16
	big := bytes.Repeat([]byte("Z"), 1<<20) // 1 MiB
	body, original, truncated, err := CapBody(bytes.NewReader(big), cap)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("truncated should be true")
	}
	if len(body) != cap {
		t.Errorf("buffered len: got %d want %d", len(body), cap)
	}
	if original != cap+1 {
		t.Errorf("original_size: got %d want %d (sentinel)", original, cap+1)
	}
}

// noReadReader wraps a base reader and trips a fatal error on any Read call
// once the base reader is exhausted. It is used to prove that CapBody does
// not drain bytes beyond capBytes+1.
type noReadReader struct {
	base  *bytes.Reader
	fired bool
	t     *testing.T
}

func (r *noReadReader) Read(p []byte) (int, error) {
	n, err := r.base.Read(p)
	if n > 0 {
		return n, err
	}
	// Base exhausted; any further Read call means CapBody read past capBytes+1.
	if !r.fired {
		r.fired = true
		r.t.Fatal("CapBody drained bytes past the cap: Read called after cap exceeded")
	}
	return 0, err
}

// TestCapBody_OversizeDoesNotDrain passes a reader that has exactly capBytes+1
// real bytes followed by a trip-wire. The trip-wire fatals if Read is called
// after the cap is exceeded, proving that no drain occurs.
func TestCapBody_OversizeDoesNotDrain(t *testing.T) {
	t.Parallel()

	const cap = 32
	// Prefix: capBytes+1 bytes of real data.
	prefix := bytes.Repeat([]byte("A"), cap+1)
	r := &noReadReader{base: bytes.NewReader(prefix), t: t}

	body, original, truncated, err := CapBody(r, cap)
	if err != nil {
		t.Fatalf("CapBody: %v", err)
	}
	if !truncated {
		t.Fatal("truncated: want true")
	}
	if len(body) != cap {
		t.Errorf("body len: got %d want %d", len(body), cap)
	}
	if original != cap+1 {
		t.Errorf("originalSize: got %d want %d", original, cap+1)
	}
}

// TestCapBody_AtCapNoTruncate verifies the boundary: body exactly equal to the
// cap is returned in full with truncated=false.
func TestCapBody_AtCapNoTruncate(t *testing.T) {
	t.Parallel()

	const cap = 24
	body := bytes.Repeat([]byte("B"), cap)
	got, original, truncated, err := CapBody(bytes.NewReader(body), cap)
	if err != nil {
		t.Fatalf("CapBody: %v", err)
	}
	if truncated {
		t.Fatal("truncated: want false for body exactly at cap")
	}
	if original != cap {
		t.Errorf("originalSize: got %d want %d", original, cap)
	}
	if !bytes.Equal(got, body) {
		t.Error("body content differs from input")
	}
}

// TestCapBody_UnderCap verifies the common case: body smaller than the cap is
// returned in full with truncated=false.
func TestCapBody_UnderCap(t *testing.T) {
	t.Parallel()

	const cap = 64
	body := []byte("small payload")
	got, original, truncated, err := CapBody(bytes.NewReader(body), cap)
	if err != nil {
		t.Fatalf("CapBody: %v", err)
	}
	if truncated {
		t.Fatal("truncated: want false")
	}
	if original != len(body) {
		t.Errorf("originalSize: got %d want %d", original, len(body))
	}
	if !bytes.Equal(got, body) {
		t.Error("body content differs from input")
	}
}

// TestCapBody_CapDisabled verifies that capBytes <= 0 reads the full body
// without truncation.
func TestCapBody_CapDisabled(t *testing.T) {
	t.Parallel()

	full := bytes.Repeat([]byte("D"), 10_000)

	for _, capBytes := range []int{0, -1} {
		t.Run(fmt.Sprintf("cap=%d", capBytes), func(t *testing.T) {
			t.Parallel()
			got, original, truncated, err := CapBody(bytes.NewReader(full), capBytes)
			if err != nil {
				t.Fatalf("CapBody: %v", err)
			}
			if truncated {
				t.Fatal("truncated: want false when cap disabled")
			}
			if original != len(full) {
				t.Errorf("originalSize: got %d want %d", original, len(full))
			}
			if !bytes.Equal(got, full) {
				t.Error("body content differs from input")
			}
		})
	}
}
