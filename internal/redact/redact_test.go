package redact

import (
	"strings"
	"testing"
)

// Fake credentials are assembled at runtime so that the secret scanner never
// sees a credential-shaped literal in this file.
func fake(prefix string, n int) string {
	return prefix + strings.Repeat("A1b2C3d4", n/8+1)[:n]
}

func TestRedactFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		secret string // must not survive redaction
		keep   string // must survive redaction
	}{
		{"github classic token", "token is " + fake("ghp_", 36), fake("ghp_", 36), "token is "},
		{"github fine-grained token", fake("github_pat_", 60), fake("github_pat_", 60), ""},
		{"openai key", "key=" + fake("sk-proj-", 40), fake("sk-proj-", 40), "key="},
		{"aws access key", "id " + "AKIA" + strings.Repeat("Z", 16), "AKIA" + strings.Repeat("Z", 16), "id "},
		{"authorization header", "Authorization: Bearer abc.def.ghi", "abc.def.ghi", "Authorization: "},
		{"bare bearer", "sent bearer " + fake("", 24), fake("", 24), "sent bearer "},
		{"url credentials", "postgres://app:hunter2secret@db.example.com:5432/app", "hunter2secret", "db.example.com"},
		{"jwt", "eyJ" + fake("", 12) + ".eyJ" + fake("", 12) + "." + fake("", 12), "eyJ" + fake("", 12), ""},
		{
			"private key block",
			"-----BEGIN RSA PRIVATE KEY-----\nMIIEow" + fake("", 32) + "\n-----END RSA PRIVATE KEY-----",
			fake("", 32), "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := Text(tt.input)
			if strings.Contains(got, tt.secret) {
				t.Fatalf("secret survived redaction: %q", got)
			}
			if !strings.Contains(got, Mask) {
				t.Fatalf("expected mask in output: %q", got)
			}
			if tt.keep != "" && !strings.Contains(got, tt.keep) {
				t.Fatalf("redaction removed context %q: %q", tt.keep, got)
			}
		})
	}
}

func TestRedactLeavesOrdinaryCodeAlone(t *testing.T) {
	t.Parallel()

	inputs := []string{
		`func (s *Service) ValidateToken(ctx context.Context, token string) error {`,
		`if authorization == nil { return ErrUnauthorized }`,
		`task-runner-for-disk-cache-with-a-long-name`,
		`https://api.github.com/repos/example-org/example-repository/pulls/1`,
		`input_tokens=1200 output_tokens=300`,
	}
	for _, in := range inputs {
		if got := Text(in); got != in {
			t.Errorf("ordinary text was modified:\n in: %q\nout: %q", in, got)
		}
	}
}

func TestRedactorLiterals(t *testing.T) {
	t.Parallel()

	secret := "correct-horse-battery-staple"
	r := New(secret, "short", "")

	got := r.Redact("env dump: OPENAI_API_KEY=" + secret + " user=short")
	if strings.Contains(got, secret) {
		t.Fatalf("literal secret survived: %q", got)
	}
	if !strings.Contains(got, "user=short") {
		t.Fatalf("short literal should be ignored, got %q", got)
	}
}

func TestRedactorLongestLiteralFirst(t *testing.T) {
	t.Parallel()

	r := New("abcdefgh", "abcdefgh-ijklmnop")
	if got := r.Redact("x abcdefgh-ijklmnop y"); got != "x *** y" {
		t.Fatalf("got %q, want %q", got, "x *** y")
	}
}

func TestNilRedactorAppliesRules(t *testing.T) {
	t.Parallel()

	var r *Redactor
	if got := r.Redact(fake("ghp_", 36)); got != Mask {
		t.Fatalf("got %q, want %q", got, Mask)
	}
}

func TestIsSensitiveKey(t *testing.T) {
	t.Parallel()

	sensitive := []string{
		"password", "DATABASE_PASSWORD", "github_token", "GITHUB_TOKEN", "OPENAI_API_KEY",
		"api-key", "apiKey", "accessToken", "Authorization", "Cookie", "Set-Cookie",
		"client_secret", "private_key", "aws.secret_key", "database_dsn", "webhook_secret",
	}
	for _, k := range sensitive {
		if !IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = false, want true", k)
		}
	}

	safe := []string{
		"input_tokens", "output_tokens", "max_tokens", "repository", "pr_number",
		"model", "execution_id", "keyword", "monkey", "author",
	}
	for _, k := range safe {
		if IsSensitiveKey(k) {
			t.Errorf("IsSensitiveKey(%q) = true, want false", k)
		}
	}
}
