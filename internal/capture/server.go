package capture

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// NewCaptureHandler routes every path through the capture pipeline; per
// ADR-0002 the response is always 202, even on drop or body-read failure.
func NewCaptureHandler(q *Queue, bodyCap int, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	r := chi.NewRouter()
	r.HandleFunc("/*", captureHandler(q, bodyCap, logger))
	return r
}

func captureHandler(q *Queue, bodyCap int, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, originalSize, truncated, err := CapBody(r.Body, bodyCap)
		if err != nil {
			logger.Warn("body read failed; record dropped", "path", r.URL.Path, "err", err)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		q.Enqueue(buildRecord(r, body, originalSize, truncated))
		w.WriteHeader(http.StatusAccepted)
	}
}

func buildRecord(r *http.Request, body []byte, originalSize int, truncated bool) *CapturedRecord {
	reqCookies := r.Cookies()
	cookies := make([]Cookie, 0, len(reqCookies))
	for _, c := range reqCookies {
		cookies = append(cookies, Cookie{Name: c.Name, Value: c.Value})
	}

	sourceIP, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil {
		sourceIP = r.RemoteAddr
	}

	return &CapturedRecord{
		ID:                uuid.NewString(),
		Timestamp:         time.Now().UTC(),
		Method:            r.Method,
		Path:              r.URL.Path,
		Query:             r.URL.Query(),
		Headers:           r.Header.Clone(),
		Cookies:           cookies,
		Body:              body,
		BodyTruncated:     truncated,
		BodyOriginalSize:  originalSize,
		ContentType:       r.Header.Get("Content-Type"),
		SourceIP:          sourceIP,
		Service:           PlaceholderService,
		ServiceSource:     PlaceholderServiceSource,
		CorrelationID:     PlaceholderCorrelationID,
		CorrelationSource: PlaceholderCorrelationSource,
	}
}
