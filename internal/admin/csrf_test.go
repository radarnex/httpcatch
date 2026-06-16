package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func makeCSRFRequest(host, secFetchSite, origin string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/auth/login", nil)
	req.Host = host
	if secFetchSite != "" {
		req.Header.Set("Sec-Fetch-Site", secFetchSite)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func csrfOKHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestIsSameOrigin_SecFetchSite_SameOrigin_Allowed(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "same-origin", "")
	if !isSameOrigin(req) {
		t.Error("expected same-origin to be allowed")
	}
}

func TestIsSameOrigin_SecFetchSite_SameSite_OriginMatches_Allowed(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "same-site", "http://admin.local:8081")
	if !isSameOrigin(req) {
		t.Error("expected same-site with matching Origin to be allowed")
	}
}

func TestIsSameOrigin_SecFetchSite_SameSite_OriginMismatches_Rejected(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "same-site", "http://evil.admin.local:8081")
	if isSameOrigin(req) {
		t.Error("expected same-site with mismatching Origin to be rejected")
	}
}

func TestIsSameOrigin_SecFetchSite_SameSite_NoOrigin_Rejected(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "same-site", "")
	if isSameOrigin(req) {
		t.Error("expected same-site without Origin to be rejected")
	}
}

func TestIsSameOrigin_SecFetchSite_None_Allowed(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "none", "")
	if !isSameOrigin(req) {
		t.Error("expected Sec-Fetch-Site: none to be allowed")
	}
}

func TestIsSameOrigin_SecFetchSite_CrossSite_Rejected(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "cross-site", "")
	if isSameOrigin(req) {
		t.Error("expected cross-site to be rejected")
	}
}

func TestIsSameOrigin_NoSecFetchSite_OriginMatches_Allowed(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "", "http://admin.local:8081")
	if !isSameOrigin(req) {
		t.Error("expected matching Origin host to be allowed")
	}
}

func TestIsSameOrigin_NoSecFetchSite_OriginMismatches_Rejected(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "", "http://evil.com")
	if isSameOrigin(req) {
		t.Error("expected mismatching Origin host to be rejected")
	}
}

func TestIsSameOrigin_NoSecFetchSite_NoOrigin_Allowed(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "", "")
	if !isSameOrigin(req) {
		t.Error("expected request with no Origin (curl path) to be allowed")
	}
}

func TestIsSameOrigin_OriginNull_Rejected(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "", "null")
	if isSameOrigin(req) {
		t.Error("expected Origin: null (sandboxed/file:// context) to be rejected")
	}
}

func TestIsSameOrigin_OriginMalformed_Rejected(t *testing.T) {
	t.Parallel()
	req := makeCSRFRequest("admin.local:8081", "", "::not-a-url::")
	if isSameOrigin(req) {
		t.Error("expected malformed Origin to be rejected")
	}
}

func TestCSRFMiddleware_Reject_Returns403_IncrementsCounter(t *testing.T) {
	t.Parallel()

	limiter := NewAuthLimiterWithClock(time.Now)
	mw := csrfOriginCheck(limiter)
	h := mw(csrfOKHandler())

	req := makeCSRFRequest("admin.local:8081", "cross-site", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status: got %d want 403", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "forbidden") {
		t.Errorf("body: got %q; want it to contain \"forbidden\"", string(body))
	}
	if got := limiter.CSRFBlockedTotal(); got != 1 {
		t.Errorf("CSRFBlockedTotal: got %d want 1", got)
	}
}

func TestCSRFMiddleware_Allow_PassesThrough(t *testing.T) {
	t.Parallel()

	limiter := NewAuthLimiterWithClock(time.Now)
	mw := csrfOriginCheck(limiter)

	reached := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	h := mw(inner)

	req := makeCSRFRequest("admin.local:8081", "same-origin", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d want 200", rec.Code)
	}
	if !reached {
		t.Error("inner handler was not called for same-origin request")
	}
	if got := limiter.CSRFBlockedTotal(); got != 0 {
		t.Errorf("CSRFBlockedTotal: got %d want 0", got)
	}
}
