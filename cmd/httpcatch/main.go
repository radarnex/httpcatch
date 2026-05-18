package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/radarnex/httpcatch/internal/app"
	"github.com/radarnex/httpcatch/internal/cli/redactcmd"
	"github.com/radarnex/httpcatch/internal/config"
)

func main() {
	args := os.Args[1:]
	sub, rest := splitSubcommand(args)
	switch sub {
	case "redact":
		os.Exit(redactcmd.Run(rest, os.Stdin, os.Stdout, os.Stderr, os.Getenv))
	case "serve", "":
		if err := serve(rest); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "httpcatch: unknown subcommand %q; valid subcommands: serve, redact\n", sub)
		os.Exit(2)
	}
}

// splitSubcommand classifies the first positional arg. The server entry point
// keeps backwards compatibility with the prior single-binary invocation: no
// args, or a leading flag, both route to `serve` with the original args
// preserved for the server's own flag set.
func splitSubcommand(args []string) (sub string, rest []string) {
	if len(args) == 0 {
		return "", nil
	}
	first := args[0]
	if strings.HasPrefix(first, "-") {
		return "", args
	}
	return first, args[1:]
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "", "path to YAML config file (optional; env overrides always apply)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath, os.Getenv)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	a, err := app.Build(cfg, logger, os.Stdout)
	if err != nil {
		return err
	}
	a.EmitStartupWarnings()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return a.Serve(ctx)
}
