package admin_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
	"github.com/radarnex/httpcatch/internal/config"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func adminCfgLoopback() config.AdminConfig {
	return config.AdminConfig{
		Bind:       "127.0.0.1:0",
		SessionTTL: 24 * time.Hour,
	}
}

func TestNew_ValidConfig_BuildsServer(t *testing.T) {
	t.Parallel()

	srv, err := admin.New(adminCfgLoopback(), discardLogger(), admin.MetricSources{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if srv == nil {
		t.Fatal("New returned nil server without error")
	}
	if srv.Router() == nil {
		t.Fatal("Router() returned nil")
	}
}

func TestNew_RefusedBind_ReturnsError(t *testing.T) {
	t.Parallel()

	cfg := config.AdminConfig{
		Bind:       "0.0.0.0:8081",
		SessionTTL: 24 * time.Hour,
	}
	_, err := admin.New(cfg, discardLogger(), admin.MetricSources{})
	if err == nil {
		t.Fatal("expected error for non-loopback bind without token or insecure flag")
	}
	if !strings.Contains(err.Error(), "admin: refuses to bind") {
		t.Errorf("error %q does not contain expected message", err.Error())
	}
}

func TestServe_Shutdown_RoundTrip(t *testing.T) {
	t.Parallel()

	srv, err := admin.New(adminCfgLoopback(), discardLogger(), admin.MetricSources{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	// Cancel to trigger graceful shutdown.
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned error after context cancel: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return within timeout after context cancel")
	}
}

func TestHealthz_Returns200Ok(t *testing.T) {
	t.Parallel()

	srv, err := admin.New(adminCfgLoopback(), discardLogger(), admin.MetricSources{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Find a free port by binding temporarily.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cfg := config.AdminConfig{
		Bind:       addr,
		SessionTTL: 24 * time.Hour,
	}
	srv, err = admin.New(cfg, discardLogger(), admin.MetricSources{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := t.Context()

	ready := make(chan struct{})
	go func() {
		close(ready)
		_ = srv.Serve(ctx)
	}()
	<-ready

	// Give the server a moment to bind.
	c := testClient(t)
	var resp *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = c.Get("http://" + addr + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body: got %q want %q", string(body), "ok")
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "text/plain; charset=utf-8" {
		t.Errorf("Content-Type: got %q want %q", ct, "text/plain; charset=utf-8")
	}
}

func TestHealthz_IgnoresBogusAuthHeader(t *testing.T) {
	t.Parallel()

	// Find a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cfg := config.AdminConfig{
		Bind:       addr,
		SessionTTL: 24 * time.Hour,
	}
	srv, err := admin.New(cfg, discardLogger(), admin.MetricSources{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := t.Context()
	go func() { _ = srv.Serve(ctx) }()

	// Wait for the server to bind.
	c := testClient(t)
	var resp *http.Response
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/healthz", nil)
		req.Header.Set("Authorization", "Bearer bogus-token-xxx")
		resp, err = c.Do(req)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /healthz with auth header: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status with bogus auth: got %d want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Errorf("body: got %q want %q", string(body), "ok")
	}
}
