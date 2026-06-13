package capture

import "io"

// CapBody reads at most capBytes+1 from r so the original size is observable
// without buffering the entire payload. capBytes <= 0 disables the cap and
// reads the body in full.
//
// When the body exceeds the cap, CapBody stops reading immediately and returns
// truncated=true with originalSize==capBytes+1. That value is a sentinel
// meaning "at least capBytes+1 bytes"; the remainder is not read or measured.
// Leaving the body unread is safe: Go's net/http post-handler logic closes the
// connection when too much body remains (bounded by its internal
// maxPostHandlerReadBytes constant), so the server never blocks on drain.
func CapBody(r io.Reader, capBytes int) (body []byte, originalSize int, truncated bool, err error) {
	if capBytes <= 0 {
		body, err = io.ReadAll(r)
		if err != nil {
			return nil, 0, false, err
		}
		return body, len(body), false, nil
	}
	body, err = io.ReadAll(io.LimitReader(r, int64(capBytes)+1))
	if err != nil {
		return nil, 0, false, err
	}
	if len(body) <= capBytes {
		return body, len(body), false, nil
	}
	// Oversize: do not drain the remainder. originalSize==capBytes+1 signals
	// "≥ capBytes+1 bytes"; the exact wire length is not known.
	return body[:capBytes], capBytes + 1, true, nil
}
