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
			name:         "over cap",
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
			got, gotOriginal, gotTrunc := CapBody(tt.body, tt.capBytes)
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
