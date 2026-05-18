package capture

import (
	"net/http"
	"testing"
)

func TestIdentifyService(t *testing.T) {
	t.Parallel()

	mk := func(pairs ...string) http.Header { return mkHeader(t, pairs...) }

	tests := []struct {
		name             string
		headers          http.Header
		configuredHeader string
		wantValue        string
		wantSource       string
	}{
		{
			name:       "default header present wins over host",
			headers:    mk("X-Httpcatch-Service", "orders", "Host", "example.com"),
			wantValue:  "orders",
			wantSource: ServiceSourceHeader,
		},
		{
			name:             "custom header name wins",
			headers:          mk("X-Service-Tag", "billing", "Host", "example.com"),
			configuredHeader: "X-Service-Tag",
			wantValue:        "billing",
			wantSource:       ServiceSourceHeader,
		},
		{
			name:             "default header ignored when custom configured",
			headers:          mk("X-Httpcatch-Service", "orders", "Host", "example.com"),
			configuredHeader: "X-Service-Tag",
			wantValue:        "example.com",
			wantSource:       ServiceSourceHost,
		},
		{
			name:       "header lookup is case-insensitive via http.Header",
			headers:    mk("x-httpcatch-service", "users"),
			wantValue:  "users",
			wantSource: ServiceSourceHeader,
		},
		{
			name:       "host with port is stripped and lowercased",
			headers:    mk("Host", "Example.com:8080"),
			wantValue:  "example.com",
			wantSource: ServiceSourceHost,
		},
		{
			name:       "host without port is lowercased",
			headers:    mk("Host", "API.Example.COM"),
			wantValue:  "api.example.com",
			wantSource: ServiceSourceHost,
		},
		{
			name:       "no headers falls through to unknown",
			headers:    mk(),
			wantValue:  UnknownService,
			wantSource: ServiceSourceUnknown,
		},
		{
			name:       "empty host string falls through to unknown",
			headers:    mk("Host", ""),
			wantValue:  UnknownService,
			wantSource: ServiceSourceUnknown,
		},
		{
			name:       "whitespace-only configured header value falls through",
			headers:    mk("X-Httpcatch-Service", "   ", "Host", "example.com"),
			wantValue:  "example.com",
			wantSource: ServiceSourceHost,
		},
		{
			name:       "ipv6 host with port is stripped",
			headers:    mk("Host", "[::1]:9000"),
			wantValue:  "::1",
			wantSource: ServiceSourceHost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotValue, gotSource := IdentifyService(tt.headers, tt.configuredHeader)
			if gotValue != tt.wantValue {
				t.Errorf("value: got %q want %q", gotValue, tt.wantValue)
			}
			if gotSource != tt.wantSource {
				t.Errorf("source: got %q want %q", gotSource, tt.wantSource)
			}
		})
	}
}
