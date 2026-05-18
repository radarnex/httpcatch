package capture

import "io"

// CapBody reads at most capBytes+1 from r so the original size is observable
// without buffering the entire payload. capBytes <= 0 disables the cap and
// reads the body in full.
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
	extra, err := io.Copy(io.Discard, r)
	if err != nil {
		return nil, 0, false, err
	}
	return body[:capBytes], capBytes + 1 + int(extra), true, nil
}
