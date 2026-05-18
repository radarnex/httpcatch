package redactcmd_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/radarnex/httpcatch/internal/capture"
	"github.com/radarnex/httpcatch/internal/cli/redactcmd"
)

func emptyEnv(string) string { return "" }

func writeSample(t *testing.T, dir string, rec capture.CapturedRecord) string {
	t.Helper()
	path := filepath.Join(dir, "sample.json")
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal sample: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	return path
}

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const fullRulesetYAML = `
redaction:
  headers:
    - authorization
  query_params:
    - token
  json_paths:
    - password
  regex:
    - name: ipv4
      pattern: '\b(?:\d{1,3}\.){3}\d{1,3}\b'
  cookies:
    - mode: redact
      names: [session_id]
`

func recordTouchingEveryRule() capture.CapturedRecord {
	return capture.CapturedRecord{
		ID:     "rec-1",
		Method: "POST",
		Path:   "/login",
		Query: map[string][]string{
			"token": {"shhh"},
			"keep":  {"ok"},
		},
		Headers: map[string][]string{
			"Authorization": {"Bearer abc"},
			"Cookie":        {"session_id=xyz; other=keep"},
			"Content-Type":  {"application/json"},
		},
		Cookies: []capture.Cookie{
			{Name: "session_id", Value: "xyz"},
			{Name: "other", Value: "keep"},
		},
		Body:        []byte(`{"password":"pw","client":"10.0.0.1"}`),
		ContentType: "application/json",
	}
}

func TestRun_Success_DiffListsAllChanges(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, fullRulesetYAML)
	samplePath := writeSample(t, dir, recordTouchingEveryRule())

	var stdout, stderr bytes.Buffer
	code := redactcmd.Run(
		[]string{"--config", cfgPath, "--test", samplePath},
		strings.NewReader(""), &stdout, &stderr, emptyEnv,
	)
	if code != 0 {
		t.Fatalf("exit code: got %d want 0, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: want empty, got %q", stderr.String())
	}

	out := stdout.String()
	wantLines := []string{
		`headers.Authorization: "Bearer abc" -> "[REDACTED]"`,
		`query.token: "shhh" -> "[REDACTED]"`,
		`cookies.session_id: xyz -> [REDACTED]`,
	}
	for _, line := range wantLines {
		if !strings.Contains(out, line) {
			t.Errorf("stdout missing %q\nfull stdout:\n%s", line, out)
		}
	}
	if !strings.Contains(out, "body:") {
		t.Errorf("stdout missing body diff line\nfull stdout:\n%s", out)
	}

	// Untouched fields must not appear as diff entries.
	for _, untouched := range []string{
		"query.keep",
		"headers.Content-Type",
		"cookies.other",
		"path:",
	} {
		if strings.Contains(out, untouched) {
			t.Errorf("stdout contains spurious entry %q\nfull stdout:\n%s", untouched, out)
		}
	}

	if !sortedLines(out) {
		t.Errorf("diff lines must be sorted for stable output, got:\n%s", out)
	}
}

func TestRun_Success_NoChanges(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "") // empty config -> empty ruleset
	samplePath := writeSample(t, dir, recordTouchingEveryRule())

	var stdout, stderr bytes.Buffer
	code := redactcmd.Run(
		[]string{"--config", cfgPath, "--test", samplePath},
		strings.NewReader(""), &stdout, &stderr, emptyEnv,
	)
	if code != 0 {
		t.Fatalf("exit code: got %d want 0, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr: want empty, got %q", stderr.String())
	}
	if stdout.String() != "no changes\n" {
		t.Errorf("stdout: want %q, got %q", "no changes\n", stdout.String())
	}
}

func TestRun_Failure_MissingSampleFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "")
	missing := filepath.Join(dir, "does-not-exist.json")

	var stdout, stderr bytes.Buffer
	code := redactcmd.Run(
		[]string{"--config", cfgPath, "--test", missing},
		strings.NewReader(""), &stdout, &stderr, emptyEnv,
	)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout: want empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Errorf("stderr should mention sample path %q, got %q", missing, stderr.String())
	}
}

func TestRun_Failure_MalformedSampleJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, "")
	samplePath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(samplePath, []byte("not-json"), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := redactcmd.Run(
		[]string{"--config", cfgPath, "--test", samplePath},
		strings.NewReader(""), &stdout, &stderr, emptyEnv,
	)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout: want empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), samplePath) {
		t.Errorf("stderr should mention sample path %q, got %q", samplePath, stderr.String())
	}
	if !strings.Contains(strings.ToLower(stderr.String()), "decode") {
		t.Errorf("stderr should mention decode error, got %q", stderr.String())
	}
}

func TestRun_Failure_MissingTestFlag(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	code := redactcmd.Run(
		[]string{},
		strings.NewReader(""), &stdout, &stderr, emptyEnv,
	)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout: want empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--test") {
		t.Errorf("stderr should mention --test, got %q", stderr.String())
	}
}

func TestRun_Failure_InvalidRuleset(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, `
redaction:
  regex:
    - name: bad
      pattern: '['
`)
	samplePath := writeSample(t, dir, recordTouchingEveryRule())

	var stdout, stderr bytes.Buffer
	code := redactcmd.Run(
		[]string{"--config", cfgPath, "--test", samplePath},
		strings.NewReader(""), &stdout, &stderr, emptyEnv,
	)
	if code == 0 {
		t.Fatalf("exit code: got 0, want non-zero")
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout: want empty, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "redaction:") {
		t.Errorf("stderr should contain the ruleset error prefix, got %q", stderr.String())
	}
}

// TestRun_Persistence_NoFilesCreated cannot run in parallel because it chdirs
// into a fresh temp directory to observe the working-directory side effects
// of the handler (or, ideally, the absence thereof).
func TestRun_Persistence_NoFilesCreated(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeConfig(t, root, fullRulesetYAML)
	samplePath := writeSample(t, root, recordTouchingEveryRule())

	preexisting := map[string]struct{}{
		filepath.Base(cfgPath):    {},
		filepath.Base(samplePath): {},
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Errorf("restore wd: %v", err)
		}
	})
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := redactcmd.Run(
		[]string{"--config", cfgPath, "--test", samplePath},
		strings.NewReader(""), &stdout, &stderr, emptyEnv,
	)
	if code != 0 {
		t.Fatalf("exit code: got %d want 0, stderr=%q", code, stderr.String())
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if _, ok := preexisting[e.Name()]; !ok {
			t.Errorf("unexpected file created in working directory: %q", e.Name())
		}
	}
}

func TestRun_StdinIsUnused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, fullRulesetYAML)
	samplePath := writeSample(t, dir, recordTouchingEveryRule())

	// Supply a reader that fails if read so we catch any accidental dependency
	// on stdin by the handler.
	stdin := failReader{t: t}
	var stdout, stderr bytes.Buffer
	code := redactcmd.Run(
		[]string{"--config", cfgPath, "--test", samplePath},
		stdin, &stdout, &stderr, emptyEnv,
	)
	if code != 0 {
		t.Fatalf("exit code: got %d want 0, stderr=%q", code, stderr.String())
	}
}

type failReader struct{ t *testing.T }

func (f failReader) Read([]byte) (int, error) {
	f.t.Errorf("handler must not read stdin")
	return 0, io.EOF
}

// sortedLines verifies that the diff lines are sorted lexicographically. It
// ignores trailing empty lines so the final newline is not interpreted as a
// reordering signal.
func sortedLines(s string) bool {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= 1 {
		return true
	}
	sorted := make([]string, len(lines))
	copy(sorted, lines)
	sort.Strings(sorted)
	for i := range lines {
		if lines[i] != sorted[i] {
			return false
		}
	}
	return true
}
