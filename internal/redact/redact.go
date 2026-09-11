// Package redact removes credentials from text before it is logged or handed to
// external services such as the LLM.
package redact

import (
	"regexp"
	"sort"
	"strings"
)

// Mask is the replacement for redacted content.
const Mask = "***"

// minLiteralLen prevents a trivially short configured secret from masking
// unrelated text everywhere it happens to appear.
const minLiteralLen = 8

type rule struct {
	re   *regexp.Regexp
	repl string
}

// rules match well-known credential formats. They are deliberately specific:
// generic "key=value" heuristics would mangle the source code the reviewer
// needs to read.
var rules = []rule{
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`), Mask},
	{regexp.MustCompile(`(?i)\b((?:proxy-)?authorization\s*[:=]\s*)(?:bearer|basic|token)\s+\S+`), "${1}" + Mask},
	{regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9\-._~+/]{16,}=*`), "${1} " + Mask},
	{regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36,}\b`), Mask},
	{regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{22,}`), Mask},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), Mask},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), Mask},
	{regexp.MustCompile(`\bxox[abposr]-[A-Za-z0-9-]{10,}`), Mask},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), Mask},
	{regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`), "${1}" + Mask + "@"},
}

// Redactor masks the exact values of known secrets plus well-known credential
// formats. The zero value and a nil *Redactor apply only the format rules.
type Redactor struct {
	literals []string
}

// New returns a Redactor that masks every occurrence of the given secret
// values. Values shorter than 8 bytes are ignored to avoid destroying
// unrelated text; such values are too weak to be real credentials anyway.
func New(secrets ...string) *Redactor {
	literals := make([]string, 0, len(secrets))
	for _, s := range secrets {
		if len(s) >= minLiteralLen {
			literals = append(literals, s)
		}
	}
	// Longest first, so a secret containing another secret is masked whole.
	sort.Slice(literals, func(i, j int) bool { return len(literals[i]) > len(literals[j]) })
	return &Redactor{literals: literals}
}

// Redact returns s with known secrets and credential-looking tokens masked.
func (r *Redactor) Redact(s string) string {
	if r != nil {
		for _, lit := range r.literals {
			s = strings.ReplaceAll(s, lit, Mask)
		}
	}
	for _, rl := range rules {
		s = rl.re.ReplaceAllString(s, rl.repl)
	}
	return s
}

// Text masks credential-looking tokens in s using the format rules only.
func Text(s string) string {
	var r *Redactor
	return r.Redact(s)
}

var (
	// sensitiveSegments are key segments that always denote a secret.
	sensitiveSegments = map[string]bool{
		"password": true, "passwd": true, "secret": true, "token": true,
		"apikey": true, "authorization": true, "cookie": true,
		"credential": true, "credentials": true, "dsn": true,
	}
	// sensitivePairs span two segments, e.g. "api_key" in "openai_api_key".
	sensitivePairs = []string{
		"api_key", "private_key", "access_key", "secret_key",
		"client_secret", "connection_string", "set_cookie",
	}
	// sensitiveSuffixes catch camelCase keys such as "accessToken" once lowercased.
	sensitiveSuffixes = []string{"token", "secret", "password", "apikey"}

	keyNormalizer = strings.NewReplacer("-", "_", ".", "_", " ", "_")
)

// IsSensitiveKey reports whether a field name (log attribute, header, config
// key) conventionally holds a secret. Matching is per segment so that
// observability fields such as "input_tokens" are not masked.
func IsSensitiveKey(key string) bool {
	k := keyNormalizer.Replace(strings.ToLower(key))
	for _, p := range sensitivePairs {
		if strings.Contains(k, p) {
			return true
		}
	}
	for _, seg := range strings.Split(k, "_") {
		if sensitiveSegments[seg] {
			return true
		}
		for _, suffix := range sensitiveSuffixes {
			if strings.HasSuffix(seg, suffix) {
				return true
			}
		}
	}
	return false
}
