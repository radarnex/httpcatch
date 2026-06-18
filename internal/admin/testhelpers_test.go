package admin_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/config"
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

// newAdminTestServer boots an in-process admin server on an httptest listener
// with the given token and read sources, and registers its shutdown.
func newAdminTestServer(t *testing.T, token string, readers admin.ReadSources) *httptest.Server {
	t.Helper()
	cfg := config.AdminConfig{
		Bind:          "127.0.0.1:0",
		Token:         token,
		SessionTTL:    time.Hour,
		SessionSecure: false,
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{}, admin.ServerOptions{Readers: readers})
	if err != nil {
		t.Fatalf("admin.New: %v", err)
	}
	ts := httptest.NewServer(srv.Router())
	t.Cleanup(ts.Close)
	return ts
}
