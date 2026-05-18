package capture

// CapBody truncates body to the first capBytes bytes when it exceeds the cap.
// A capBytes of 0 disables truncation; the body is returned unchanged.
// Returns the (possibly truncated) body, the original size before any truncation,
// and whether truncation occurred.
func CapBody(body []byte, capBytes int) (capped []byte, originalSize int, truncated bool) {
	originalSize = len(body)
	if capBytes <= 0 || originalSize <= capBytes {
		return body, originalSize, false
	}
	return body[:capBytes], originalSize, true
}
