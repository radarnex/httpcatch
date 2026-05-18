package admin

import "net/http"

// healthzHandler responds with 200 OK and body "ok". It is registered directly
// on the root router and never wrapped in middleware so that load balancers and
// Kubernetes probes can reach it without credentials.
func healthzHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
