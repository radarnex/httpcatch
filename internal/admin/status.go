package admin

import (
	"encoding/json"
	"net/http"

	"github.com/radarnex/httpcatch/internal/buildinfo"
)

type statusResponse struct {
	Unredacted bool           `json:"unredacted"`
	Counters   statusCounters `json:"counters"`
	Version    string         `json:"version"`
	BuildTime  string         `json:"build_time"`
}

type statusCounters struct {
	DroppedTotal                    uint64 `json:"dropped_total"`
	CapturedWithoutCorrelationTotal uint64 `json:"captured_without_correlation_total"`
	CapturedWithoutServiceTotal     uint64 `json:"captured_without_service_total"`
	RedactionErrorsTotal            uint64 `json:"redaction_errors_total"`
}

// statusHandler returns an http.HandlerFunc that serves the current process
// state as JSON. The UI polls this endpoint every 5 seconds to keep banners
// and counters live. The endpoint is authenticated; auth failures are handled
// by the surrounding middleware before this handler is reached.
func statusHandler(src MetricSources) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		resp := statusResponse{
			Unredacted: src.Unredacted(),
			Counters: statusCounters{
				DroppedTotal:                    src.DroppedTotal(),
				CapturedWithoutCorrelationTotal: src.CapturedWithoutCorrelationTotal(),
				CapturedWithoutServiceTotal:     src.CapturedWithoutServiceTotal(),
				RedactionErrorsTotal:            src.RedactionErrorsTotal(),
			},
			Version:   buildinfo.Version,
			BuildTime: buildinfo.BuildTime,
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
