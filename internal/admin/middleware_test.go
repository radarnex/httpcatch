package admin_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/radarnex/httpcatch/internal/admin"
)

const testAdminToken = "super-secret-token-for-tests"

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func storeWithSession(t *testing.T) (*admin.SessionStore, admin.Session) {
	t.Helper()
	store := admin.NewSessionStore(time.Now)
	sess, err := store.Create(time.Hour)
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	return store, sess
}

// noopLimiter returns a limiter that never rate-limits (full buckets, no failures).
func noopLimiter() *admin.AuthLimiter {
	return admin.NewAuthLimiter()
}

func TestMiddleware_NoAuth_Returns401(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	mw := admin.Middleware(testAdminToken, store, noopLimiter())
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", rec.Code)
	}
	wwwAuth := rec.Header().Get("WWW-Authenticate")
	if wwwAuth != `Bearer realm="httpcatch"` {
		t.Errorf("WWW-Authenticate: got %q", wwwAuth)
	}
}

func TestMiddleware_ValidBearer_Returns200(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	mw := admin.Middleware(testAdminToken, store, noopLimiter())
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d want 200", rec.Code)
	}
}

func TestMiddleware_InvalidBearer_Returns401(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	mw := admin.Middleware(testAdminToken, store, noopLimiter())
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", rec.Code)
	}
}

func TestMiddleware_ValidCookie_Returns200(t *testing.T) {
	t.Parallel()

	store, sess := storeWithSession(t)
	mw := admin.Middleware(testAdminToken, store, noopLimiter())
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.AddCookie(&http.Cookie{Name: "httpcatch_session", Value: sess.ID})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d want 200", rec.Code)
	}
}

func TestMiddleware_InvalidCookie_Returns401(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	mw := admin.Middleware(testAdminToken, store, noopLimiter())
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.AddCookie(&http.Cookie{Name: "httpcatch_session", Value: "bogus-session-id"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", rec.Code)
	}
}

func TestMiddleware_ValidBearerAndValidCookie_Returns200(t *testing.T) {
	t.Parallel()

	store, sess := storeWithSession(t)
	mw := admin.Middleware(testAdminToken, store, noopLimiter())
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	req.AddCookie(&http.Cookie{Name: "httpcatch_session", Value: sess.ID})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d want 200", rec.Code)
	}
}

func TestMiddleware_NoCookieAuth_CookieOnly_Returns401(t *testing.T) {
	t.Parallel()

	store, sess := storeWithSession(t)
	mw := admin.Middleware(testAdminToken, store, noopLimiter(), admin.WithCookieAuth(false))
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.AddCookie(&http.Cookie{Name: "httpcatch_session", Value: sess.ID})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", rec.Code)
	}
}

func TestMiddleware_NoCookieAuth_ValidBearer_Returns200(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	mw := admin.Middleware(testAdminToken, store, noopLimiter(), admin.WithCookieAuth(false))
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.Header.Set("Authorization", "Bearer "+testAdminToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d want 200", rec.Code)
	}
}

func TestMiddleware_HTMLClient_Failure_Redirects303(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	mw := admin.Middleware(testAdminToken, store, noopLimiter())
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping?foo=bar", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Errorf("status: got %d want 303", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc != "/login?next=%2Fadmin%2Fping%3Ffoo%3Dbar" {
		t.Errorf("Location: got %q", loc)
	}
}

func TestMiddleware_StarStar_NotTreatedAsHTML(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	mw := admin.Middleware(testAdminToken, store, noopLimiter())
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.Header.Set("Accept", "*/*")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// */* must not trigger 303; should get 401.
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401 for Accept: */*", rec.Code)
	}
}

func TestMiddleware_EmptyToken_AlwaysDenies(t *testing.T) {
	t.Parallel()

	store := admin.NewSessionStore(time.Now)
	mw := admin.Middleware("", store, noopLimiter())
	h := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/admin/ping", nil)
	req.Header.Set("Authorization", "Bearer ")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("empty-token + empty-bearer: got %d want 401", rec.Code)
	}

	body, _ := io.ReadAll(rec.Body)
	if string(body) != "unauthorized\n" {
		t.Errorf("body: got %q want %q", string(body), "unauthorized\n")
	}
}
