// Package buildinfo carries link-time identity for the running binary. The
// vars below are populated via -ldflags -X at build time; they default to
// values that are useful during `go run` and `go build` without flags.
package buildinfo

var (
	Version   = "dev"
	BuildTime = "unknown"
)
