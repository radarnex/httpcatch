package app_test

import (
	"net/http"
	"testing"
	"time"
)

// testClient returns an http.Client backed by a dedicated transport rather than
// the process-global http.DefaultTransport, so parallel tests never share a
// connection pool. Idle connections are closed when the test ends. The per-request
// timeout bounds a hung server so the test fails fast instead of riding the whole
// `go test` deadline.
func testClient(t *testing.T) *http.Client {
	t.Helper()
	tr := &http.Transport{}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

// noFollowClient returns a testClient that surfaces the first redirect response
// instead of following it.
func noFollowClient(t *testing.T) *http.Client {
	c := testClient(t)
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return c
}
