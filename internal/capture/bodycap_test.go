package capture

import (
	"bytes"
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
			wantOriginal: 6,
			wantTrunc:    true,
		},
		{
			name:         "many bytes over cap",
			body:         []byte("hello world"),
			capBytes:     5,
			wantBody:     []byte("hello"),
			wantOriginal: 11,
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

// TestCapBody_StreamsRemainderWithoutBuffering proves that bytes past the cap
// do not enter the returned slice — load-bearing for memory bounds.
func TestCapBody_StreamsRemainderWithoutBuffering(t *testing.T) {
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
	if original != len(big) {
		t.Errorf("original_size: got %d want %d", original, len(big))
	}
}
