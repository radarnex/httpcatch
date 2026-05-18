// Package redactcmd implements the in-process handler for the
// `httpcatch redact --test <sample-file>` dry-run subcommand. It loads the
// configured ruleset, applies it to a JSON-encoded CapturedRecord read from
// disk, and writes a before/after diff to stdout. Nothing is persisted: no
// sink is constructed, no SQLite file is opened, no startup logs reach the
// operational sink.
package redactcmd

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/config"
	"github.com/radarnex/httpcatch/internal/redact"
)

// Run is the in-process entry point for the redact subcommand. It returns the
// exit code (0 on success, non-zero on error) so the caller (main) can map it
// to os.Exit. Errors are written to stderr inside Run.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer, env func(string) string) int {
	fs := flag.NewFlagSet("redact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	samplePath := fs.String("test", "", "path to a JSON-encoded CapturedRecord to apply the ruleset against")
	configPath := fs.String("config", "", "path to YAML config file (optional; env overrides always apply)")

	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError writes its own message to stderr (which we
		// already redirected). A non-nil err from Parse — including
		// flag.ErrHelp — should map to a non-zero exit because the operator
		// did not supply a runnable invocation.
		if errors.Is(err, flag.ErrHelp) {
			return 2
		}
		return 2
	}

	if *samplePath == "" {
		fmt.Fprintln(stderr, "redact: --test <sample-file> is required")
		return 2
	}

	if env == nil {
		env = os.Getenv
	}
	cfg, err := config.Load(*configPath, env)
	if err != nil {
		fmt.Fprintf(stderr, "redact: load config: %v\n", err)
		return 1
	}

	ruleset, err := redact.NewRuleset(cfg.Redaction)
	if err != nil {
		fmt.Fprintf(stderr, "redact: %v\n", err)
		return 1
	}

	data, err := os.ReadFile(*samplePath)
	if err != nil {
		fmt.Fprintf(stderr, "redact: read sample %q: %v\n", *samplePath, err)
		return 1
	}

	var record capture.CapturedRecord
	if err := json.Unmarshal(data, &record); err != nil {
		fmt.Fprintf(stderr, "redact: decode sample %q: %v\n", *samplePath, err)
		return 1
	}

	before := cloneRecord(&record)
	after := ruleset.Redact(&record)

	entries := diffRecords(before, after)
	if err := renderDiff(entries, stdout); err != nil {
		fmt.Fprintf(stderr, "redact: write diff: %v\n", err)
		return 1
	}
	return 0
}

// cloneRecord deep-copies the maps, cookie slice, and body so that mutations
// performed by ruleset.Redact on the input record do not contaminate the
// before-side of the diff. The capture.Record value itself is copied by
// value; only the heap-shared substructures need fresh allocations.
func cloneRecord(rec *capture.CapturedRecord) *capture.CapturedRecord {
	out := *rec
	out.Headers = cloneStringSliceMap(rec.Headers)
	out.Query = cloneStringSliceMap(rec.Query)
	if rec.Cookies != nil {
		out.Cookies = make([]capture.Cookie, len(rec.Cookies))
		copy(out.Cookies, rec.Cookies)
	}
	if rec.Body != nil {
		out.Body = make([]byte, len(rec.Body))
		copy(out.Body, rec.Body)
	}
	return &out
}

func cloneStringSliceMap(m map[string][]string) map[string][]string {
	if m == nil {
		return nil
	}
	out := make(map[string][]string, len(m))
	for k, vs := range m {
		vsCopy := make([]string, len(vs))
		copy(vsCopy, vs)
		out[k] = vsCopy
	}
	return out
}
