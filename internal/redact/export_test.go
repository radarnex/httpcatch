package redact

// IsTextLikeContentType exposes the body-application gate to the external
// _test package so the classifier can be table-tested independently of the
// regex bucket walk.
func IsTextLikeContentType(ct string) bool { return isTextLikeContentType(ct) }
