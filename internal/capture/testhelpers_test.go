package capture

import (
	"net/http"
	"testing"
)

// mkHeader builds an http.Header from alternating key/value pairs, going
// through Set so the keys land on their canonical MIME form — same shape
// r.Header arrives in server-side.
func mkHeader(t *testing.T, pairs ...string) http.Header {
	t.Helper()
	if len(pairs)%2 != 0 {
		t.Fatalf("mkHeader: odd pair count %d", len(pairs))
	}
	h := http.Header{}
	for i := 0; i < len(pairs); i += 2 {
		h.Set(pairs[i], pairs[i+1])
	}
	return h
}
