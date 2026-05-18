package capture

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type HandlerOptions struct {
	Queue         *Queue
	Counters      *Counters
	ServiceHeader string
	BodyCap       int
	Logger        *slog.Logger
}

// NewCaptureHandler routes every path through the capture pipeline; per
// ADR-0002 the response is always 202, even on drop or body-read failure.
func NewCaptureHandler(opts HandlerOptions) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	r := chi.NewRouter()
	r.HandleFunc("/*", captureHandler(opts))
	return r
}

func captureHandler(opts HandlerOptions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		body, originalSize, truncated, err := CapBody(r.Body, opts.BodyCap)
		if err != nil {
			opts.Logger.Warn("body read failed; record dropped", "path", r.URL.Path, "err", err)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		opts.Queue.Enqueue(buildRecord(r, body, originalSize, truncated, opts.Counters, opts.ServiceHeader))
		w.WriteHeader(http.StatusAccepted)
	}
}

func buildRecord(r *http.Request, body []byte, originalSize int, truncated bool, counters *Counters, serviceHeader string) *CapturedRecord {
	reqCookies := r.Cookies()
	cookies := make([]Cookie, 0, len(reqCookies))
	for _, c := range reqCookies {
		cookies = append(cookies, Cookie{Name: c.Name, Value: c.Value})
	}

	sourceIP, _, splitErr := net.SplitHostPort(r.RemoteAddr)
	if splitErr != nil {
		sourceIP = r.RemoteAddr
	}

	// Single clone serves both identifier lookup and the stored record;
	// stamping r.Host onto it makes the routing host visible to operators
	// who scan headers in the captured payload.
	headers := r.Header.Clone()
	if r.Host != "" {
		headers.Set(HostHeader, r.Host)
	}

	service, serviceSource := IdentifyService(headers, serviceHeader)
	if serviceSource == ServiceSourceUnknown {
		counters.incWithoutService()
	}

	correlationID, correlationSource := IdentifyCorrelation(headers)
	if correlationSource == CorrelationSourceSynthesized {
		counters.incWithoutCorrelation()
	}

	return &CapturedRecord{
		ID:                uuid.NewString(),
		Timestamp:         time.Now().UTC(),
		Method:            r.Method,
		Path:              r.URL.Path,
		Query:             r.URL.Query(),
		Headers:           headers,
		Cookies:           cookies,
		Body:              body,
		BodyTruncated:     truncated,
		BodyOriginalSize:  originalSize,
		ContentType:       r.Header.Get("Content-Type"),
		SourceIP:          sourceIP,
		Service:           service,
		ServiceSource:     serviceSource,
		CorrelationID:     correlationID,
		CorrelationSource: correlationSource,
	}
}
