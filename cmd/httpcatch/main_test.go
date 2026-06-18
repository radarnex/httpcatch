package main

import (
	"slices"
	"testing"
)

func TestSplitSubcommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantSub  string
		wantRest []string
	}{
		{name: "nil args route to serve", args: nil, wantSub: "", wantRest: nil},
		{name: "empty args route to serve", args: []string{}, wantSub: "", wantRest: nil},
		{name: "leading single-dash flag routes to serve with args preserved", args: []string{"-config", "c.yaml"}, wantSub: "", wantRest: []string{"-config", "c.yaml"}},
		{name: "leading double-dash flag routes to serve with args preserved", args: []string{"--help"}, wantSub: "", wantRest: []string{"--help"}},
		{name: "serve with no further args", args: []string{"serve"}, wantSub: "serve", wantRest: []string{}},
		{name: "serve with flags", args: []string{"serve", "-config", "c.yaml"}, wantSub: "serve", wantRest: []string{"-config", "c.yaml"}},
		{name: "redact with a positional arg", args: []string{"redact", "in.json"}, wantSub: "redact", wantRest: []string{"in.json"}},
		{name: "unknown subcommand is returned verbatim", args: []string{"bogus", "x"}, wantSub: "bogus", wantRest: []string{"x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotSub, gotRest := splitSubcommand(tt.args)
			if gotSub != tt.wantSub {
				t.Errorf("sub: got %q want %q", gotSub, tt.wantSub)
			}
			if !slices.Equal(gotRest, tt.wantRest) {
				t.Errorf("rest: got %#v want %#v", gotRest, tt.wantRest)
			}
		})
	}
}

// The empty-args contract specifically returns a nil slice (not an empty
// non-nil slice); slices.Equal cannot distinguish the two, so assert it
// directly.
func TestSplitSubcommand_EmptyArgsReturnsNilRest(t *testing.T) {
	t.Parallel()

	if _, rest := splitSubcommand(nil); rest != nil {
		t.Errorf("rest for nil args: got %#v want nil", rest)
	}
	if _, rest := splitSubcommand([]string{}); rest != nil {
		t.Errorf("rest for empty args: got %#v want nil", rest)
	}
}
