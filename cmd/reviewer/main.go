// Command reviewer runs the Sentinel AI code review agent against a pull request.
package main

import (
	"flag"
	"fmt"
	"os"

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

	logger := logging.New(os.Stderr, cfg.Log.Level, cfg.Log.Format, redact.New(cfg.Secrets()...))
	logger.Info("configuration loaded", "config", cfg)
	logger.Warn("review pipeline not implemented yet; see the roadmap in README.md")
	return nil
}
