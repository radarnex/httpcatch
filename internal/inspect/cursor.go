package inspect

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cursor encodes a stable pagination position as (timestamp DESC, id DESC).
// The wire format is base64(unix-nano-timestamp ":" id).
type Cursor struct {
	Timestamp time.Time
	ID        string
}

// Encode encodes the cursor to its base64 wire representation.
func (c Cursor) Encode() string {
	raw := fmt.Sprintf("%d:%s", c.Timestamp.UnixNano(), c.ID)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a base64-encoded cursor string. Returns a non-nil error
// for any of: bad base64, missing colon separator, non-integer timestamp part.
func DecodeCursor(s string) (*Cursor, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("cursor: invalid base64: %w", err)
	}
	raw := string(b)
	idx := strings.IndexByte(raw, ':')
	if idx < 0 {
		return nil, fmt.Errorf("cursor: missing ':' separator")
	}
	nanos, err := strconv.ParseInt(raw[:idx], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("cursor: non-integer timestamp part: %w", err)
	}
	id := raw[idx+1:]
	if id == "" {
		return nil, fmt.Errorf("cursor: empty id part")
	}
	return &Cursor{
		Timestamp: time.Unix(0, nanos).UTC(),
		ID:        id,
	}, nil
}
