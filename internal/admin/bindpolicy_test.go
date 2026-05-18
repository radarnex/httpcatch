package admin_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/radarnex/httpcatch/internal/admin"
)

func TestGuard(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		bind           string
		tokenConfigured bool
		insecureListen bool
		wantReason     admin.Reason
		wantErr        bool
		wantErrSubstr  string
	}{
		// PRD 4-row table
		{
			name:           "loopback-ipv4-no-token",
			bind:           "127.0.0.1:8081",
			tokenConfigured: false,
			insecureListen: false,
			wantReason:     admin.ReasonLoopbackDefault,
		},
		{
			name:           "loopback-ipv4-with-token",
			bind:           "127.0.0.1:8081",
			tokenConfigured: true,
			insecureListen: false,
			wantReason:     admin.ReasonLoopbackDefault,
		},
		{
			name:           "non-loopback-with-token",
			bind:           "0.0.0.0:8081",
			tokenConfigured: true,
			insecureListen: false,
			wantReason:     admin.ReasonTokenConfigured,
		},
		{
			name:           "non-loopback-no-token-insecure",
			bind:           "0.0.0.0:8081",
			tokenConfigured: false,
			insecureListen: true,
			wantReason:     admin.ReasonInsecureMode,
		},
		{
			name:           "non-loopback-no-token-no-insecure-refused",
			bind:           "0.0.0.0:8081",
			tokenConfigured: false,
			insecureListen: false,
			wantErr:        true,
			wantErrSubstr:  `admin: refuses to bind "0.0.0.0:8081"`,
		},
		// localhost treated as loopback
		{
			name:           "localhost-no-token",
			bind:           "localhost:8081",
			tokenConfigured: false,
			insecureListen: false,
			wantReason:     admin.ReasonLoopbackDefault,
		},
		// IPv6 loopback
		{
			name:           "ipv6-loopback",
			bind:           "[::1]:8081",
			tokenConfigured: false,
			insecureListen: false,
			wantReason:     admin.ReasonLoopbackDefault,
		},
		// refusal error names both remediations
		{
			name:           "refusal-mentions-both-remediations",
			bind:           "192.168.1.1:8081",
			tokenConfigured: false,
			insecureListen: false,
			wantErr:        true,
			wantErrSubstr:  "admin.token",
		},
		{
			name:           "refusal-mentions-insecure-listen",
			bind:           "10.0.0.1:8081",
			tokenConfigured: false,
			insecureListen: false,
			wantErr:        true,
			wantErrSubstr:  "admin.insecure_listen=true",
		},
		// invalid bind address
		{
			name:    "invalid-bind-no-port",
			bind:    "notanaddress",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			reason, err := admin.Guard(tc.bind, tc.tokenConfigured, tc.insecureListen)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("Guard(%q, %v, %v): expected error, got nil (reason=%q)",
						tc.bind, tc.tokenConfigured, tc.insecureListen, reason)
				}
				if tc.wantErrSubstr != "" && !strings.Contains(err.Error(), tc.wantErrSubstr) {
					t.Errorf("Guard error %q does not contain %q", err.Error(), tc.wantErrSubstr)
				}
				return
			}

			if err != nil {
				t.Fatalf("Guard(%q, %v, %v): unexpected error: %v",
					tc.bind, tc.tokenConfigured, tc.insecureListen, err)
			}
			if reason != tc.wantReason {
				t.Errorf("Guard(%q, %v, %v): reason got %q want %q",
					tc.bind, tc.tokenConfigured, tc.insecureListen, reason, tc.wantReason)
			}
		})
	}
}

func TestGuard_InvalidBind_WrapsErrInvalidBind(t *testing.T) {
	t.Parallel()

	_, err := admin.Guard("notanaddress", false, false)
	if err == nil {
		t.Fatal("expected error for unparseable bind address")
	}
	if !errors.Is(err, admin.ErrInvalidBind) {
		t.Errorf("expected errors.Is(err, ErrInvalidBind), got: %v", err)
	}
}
