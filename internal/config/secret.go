package config

import (
	"fmt"
	"io"
	"log/slog"
)

const redacted = "***"

// Secret holds a credential. Every formatting path (fmt verbs, JSON, text
// encoding, slog) renders it as "***"; the raw value is only reachable through
// an explicit call to Reveal, which keeps accidental leaks easy to spot in review.
type Secret string

// Reveal returns the raw secret value. Call it only at the point of use
// (e.g. when building an Authorization header), never for logging.
func (s Secret) Reveal() string { return string(s) }

// IsZero reports whether the secret is empty.
func (s Secret) IsZero() bool { return s == "" }

// String implements fmt.Stringer.
func (Secret) String() string { return redacted }

// Format implements fmt.Formatter so that every verb, including %#v and
// mismatched verbs like %d, prints the mask instead of the value.
func (Secret) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redacted) }

// MarshalJSON implements json.Marshaler.
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"` + redacted + `"`), nil }

// MarshalText implements encoding.TextMarshaler.
func (Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// LogValue implements slog.LogValuer.
func (Secret) LogValue() slog.Value { return slog.StringValue(redacted) }
