// Package logging builds the structured logger used across the service. Every
// record passes through redaction, so credentials never reach a log sink even
// when a caller logs something it should not have.
package logging

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/OtavioMendes12/Sentinel-AI/internal/redact"
)

// Supported output formats.
const (
	FormatJSON = "json"
	FormatText = "text"
)

// New returns a logger writing to w in the given format ("json" or "text").
// Attributes with sensitive names are masked, and string values, errors and
// Stringers are scrubbed with r. Structured values (maps, structs) are not
// inspected, so log individual fields rather than whole requests or headers.
func New(w io.Writer, level slog.Leveler, format string, r *redact.Redactor) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: replaceAttr(r)}

	var h slog.Handler
	if format == FormatText {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h)
}

func replaceAttr(r *redact.Redactor) func([]string, slog.Attr) slog.Attr {
	return func(_ []string, a slog.Attr) slog.Attr {
		if redact.IsSensitiveKey(a.Key) {
			return slog.String(a.Key, redact.Mask)
		}
		switch a.Value.Kind() {
		case slog.KindString:
			return slog.String(a.Key, r.Redact(a.Value.String()))
		case slog.KindAny:
			switch v := a.Value.Any().(type) {
			case error:
				return slog.String(a.Key, r.Redact(v.Error()))
			case fmt.Stringer:
				return slog.String(a.Key, r.Redact(v.String()))
			}
		}
		return a
	}
}
