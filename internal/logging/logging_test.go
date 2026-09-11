package logging

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/OtavioMendes12/Sentinel-AI/internal/redact"
)

func TestLoggerRedacts(t *testing.T) {
	t.Parallel()

	const configured = "configured-secret-value-123"
	ghToken := "ghp_" + strings.Repeat("x", 36)

	for _, format := range []string{FormatJSON, FormatText} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer
			log := New(&buf, slog.LevelDebug, format, redact.New(configured))

			log.Info("calling api with "+ghToken,
				"password", "plain-password-value",
				"headers_authorization", "Bearer "+configured,
				"note", "value is "+configured,
				"err", fmt.Errorf("dial: %w", errors.New("postgres://app:db-pass-value@localhost/app")),
				"input_tokens", 1200,
			)
			log.With("github_token", ghToken).WithGroup("request").Warn("retrying", "token", "t0k3n-value-xyz")

			out := buf.String()
			for _, leaked := range []string{configured, ghToken, "plain-password-value", "db-pass-value", "t0k3n-value-xyz"} {
				if strings.Contains(out, leaked) {
					t.Errorf("log output leaks %q:\n%s", leaked, out)
				}
			}
			if !strings.Contains(out, "1200") {
				t.Errorf("non-sensitive numeric field was dropped:\n%s", out)
			}
		})
	}
}

func TestLoggerRespectsLevel(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	log := New(&buf, slog.LevelWarn, FormatJSON, nil)
	log.Info("hidden")
	log.Warn("shown")

	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Fatalf("unexpected output: %s", buf.String())
	}
}
