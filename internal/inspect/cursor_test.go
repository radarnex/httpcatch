package inspect_test

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/inspect"
)

func TestCursor_EncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 5, 18, 12, 0, 0, 123456789, time.UTC)
	original := inspect.Cursor{Timestamp: ts, ID: "abc-def-123"}

	encoded := original.Encode()
	if encoded == "" {
		t.Fatal("Encode returned empty string")
	}

	decoded, err := inspect.DecodeCursor(encoded)
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if !decoded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp: got %v want %v", decoded.Timestamp, original.Timestamp)
	}
	if decoded.ID != original.ID {
		t.Errorf("ID: got %q want %q", decoded.ID, original.ID)
	}
}

func TestDecodeCursor_BadBase64(t *testing.T) {
	t.Parallel()

	_, err := inspect.DecodeCursor("not-valid-base64!!!")
	if err == nil {
		t.Fatal("expected error for bad base64")
	}
}

func TestDecodeCursor_MissingColon(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte("1234567890"))
	_, err := inspect.DecodeCursor(encoded)
	if err == nil {
		t.Fatal("expected error for missing colon separator")
	}
}

func TestDecodeCursor_NonIntegerTimestamp(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte("notanumber:someid"))
	_, err := inspect.DecodeCursor(encoded)
	if err == nil {
		t.Fatal("expected error for non-integer timestamp")
	}
}

func TestDecodeCursor_EmptyID(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte("1234567890:"))
	_, err := inspect.DecodeCursor(encoded)
	if err == nil {
		t.Fatal("expected error for empty id")
	}
}

func TestCursor_TimestampNanosPreserved(t *testing.T) {
	t.Parallel()

	// Nanosecond precision must survive the encode/decode round-trip.
	ts := time.Unix(1747569600, 987654321).UTC()
	c := inspect.Cursor{Timestamp: ts, ID: "id-with-colons:inside"}
	decoded, err := inspect.DecodeCursor(c.Encode())
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if decoded.Timestamp.UnixNano() != ts.UnixNano() {
		t.Errorf("nanos: got %d want %d", decoded.Timestamp.UnixNano(), ts.UnixNano())
	}
	if decoded.ID != c.ID {
		t.Errorf("ID: got %q want %q", decoded.ID, c.ID)
	}
}
