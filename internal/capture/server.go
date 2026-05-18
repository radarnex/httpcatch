package capture

import (
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// NewCaptureHandler builds the capture port's HTTP handler.
// Every request to any path is captured and acked with 202 Accepted.
func NewCaptureHandler(q *Queue, bodyCap int) http.Handler {
	r := chi.NewRouter()
	r.HandleFunc("/*", captureHandler(q, bodyCap))
	return r
}

func captureHandler(q *Queue, bodyCap int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		rec, err := buildRecord(r, bodyCap)
		if err == nil && rec != nil {
			q.Enqueue(rec)
		}
		w.WriteHeader(http.StatusAccepted)
	}
}

func buildRecord(r *http.Request, bodyCap int) (*CapturedRecord, error) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	body, originalSize, truncated := CapBody(bodyBytes, bodyCap)

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
	}, nil
}
