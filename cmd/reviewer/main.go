// Command reviewer runs the Sentinel AI code review agent against a pull request.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/OtavioMendes12/Sentinel-AI/internal/config"
	"github.com/OtavioMendes12/Sentinel-AI/internal/logging"
	"github.com/OtavioMendes12/Sentinel-AI/internal/redact"
)

func main() {
	envFile := flag.String("env-file", "", "path to a .env file for local development; the real environment takes precedence")
	flag.Parse()

	if err := run(*envFile); err != nil {
		// Config errors never include secret values; redact anyway as a last line of defense.
		fmt.Fprintln(os.Stderr, "sentinel-reviewer:", redact.Text(err.Error()))
		os.Exit(1)
	}
}

func run(envFile string) error {
	cfg, err := config.Load(envFile)
	if err != nil {
		return fmt.Errorf("loading configuration:\n%w", err)
	}

	redactor := redact.New(cfg.Secrets()...)
	logger := logging.New(os.Stderr, cfg.Log.Level, cfg.Log.Format, redactor)
	logger.Debug("configuration loaded", "config", cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := review(ctx, cfg, logger, redactor, os.Stdout); err != nil {
		return fmt.Errorf("%s", redactor.Redact(err.Error()))
	}
	return nil
}
