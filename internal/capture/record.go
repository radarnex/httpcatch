package capture

import "time"

const (
	PlaceholderService           = "placeholder"
	PlaceholderServiceSource     = "placeholder"
	PlaceholderCorrelationID     = "placeholder"
	PlaceholderCorrelationSource = "placeholder"
)

type Cookie struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type CapturedRecord struct {
	ID                string              `json:"id"`
	Timestamp         time.Time           `json:"timestamp"`
	Method            string              `json:"method"`
	Path              string              `json:"path"`
	Query             map[string][]string `json:"query"`
	Headers           map[string][]string `json:"headers"`
	Cookies           []Cookie            `json:"cookies"`
	Body              []byte              `json:"body"`
	BodyTruncated     bool                `json:"body_truncated"`
	BodyOriginalSize  int                 `json:"body_original_size"`
	ContentType       string              `json:"content_type"`
	SourceIP          string              `json:"source_ip"`
	Service           string              `json:"service"`
	ServiceSource     string              `json:"service_source"`
	CorrelationID     string              `json:"correlation_id"`
	CorrelationSource string              `json:"correlation_source"`
}
