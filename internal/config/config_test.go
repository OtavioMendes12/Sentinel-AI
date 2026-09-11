package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fictitious credentials. They are not in any real token format, so neither
// the secret scanner nor a reader should mistake them for real ones.
const (
	testOpenAIKey   = "test-openai-key-0000000000"
	testGitHubToken = "test-github-token-0000000000"
)

func validEnv() map[string]string {
	return map[string]string{
		"OPENAI_API_KEY":    testOpenAIKey,
		"GITHUB_TOKEN":      testGitHubToken,
		"GITHUB_REPOSITORY": "example-org/example-repository",
		"PR_NUMBER":         "42",
	}
}

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestFromLookupDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := FromLookup(lookupFrom(validEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.OpenAI.APIKey.Reveal() != testOpenAIKey {
		t.Error("OpenAI API key not loaded")
	}
	if cfg.GitHub.Token.Reveal() != testGitHubToken {
		t.Error("GitHub token not loaded")
	}
	if cfg.GitHub.Repository != "example-org/example-repository" || cfg.GitHub.PRNumber != 42 {
		t.Errorf("unexpected pull request target: %s#%d", cfg.GitHub.Repository, cfg.GitHub.PRNumber)
	}
	if cfg.OpenAI.Model != defaultOpenAIModel || cfg.OpenAI.BaseURL != defaultOpenAIBaseURL {
		t.Errorf("unexpected OpenAI defaults: %+v", cfg.OpenAI)
	}
	if cfg.Agent.MaxSteps != 10 || cfg.Agent.MinConfidence != 0.75 || cfg.Agent.Timeout != 5*time.Minute {
		t.Errorf("unexpected agent defaults: %+v", cfg.Agent)
	}
	if cfg.Log.Level != slog.LevelInfo || cfg.Log.Format != "json" {
		t.Errorf("unexpected log defaults: %+v", cfg.Log)
	}
	if cfg.DryRun {
		t.Error("DryRun should default to false")
	}
}

func TestFromLookupOverrides(t *testing.T) {
	t.Parallel()

	env := validEnv()
	env["OPENAI_BASE_URL"] = "http://localhost:8080/v1/"
	env["MAX_AGENT_STEPS"] = "25"
	env["MIN_CONFIDENCE"] = "0.9"
	env["AGENT_TIMEOUT"] = "90s"
	env["LOG_LEVEL"] = "debug"
	env["LOG_FORMAT"] = "TEXT"
	env["DRY_RUN"] = "true"

	cfg, err := FromLookup(lookupFrom(env))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OpenAI.BaseURL != "http://localhost:8080/v1" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", cfg.OpenAI.BaseURL)
	}
	if cfg.Agent.MaxSteps != 25 || cfg.Agent.MinConfidence != 0.9 || cfg.Agent.Timeout != 90*time.Second {
		t.Errorf("unexpected agent config: %+v", cfg.Agent)
	}
	if cfg.Log.Level != slog.LevelDebug || cfg.Log.Format != "text" || !cfg.DryRun {
		t.Errorf("unexpected overrides: %+v dryRun=%v", cfg.Log, cfg.DryRun)
	}
}

func TestFromLookupReportsAllMissingRequired(t *testing.T) {
	t.Parallel()

	_, err := FromLookup(lookupFrom(map[string]string{"OPENAI_API_KEY": "   "}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, key := range []string{"OPENAI_API_KEY", "GITHUB_TOKEN", "GITHUB_REPOSITORY", "PR_NUMBER"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s: %v", key, err)
		}
	}
}

func TestFromLookupInvalidValues(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"MIN_CONFIDENCE":    "1.5",
		"MAX_AGENT_STEPS":   "0",
		"AGENT_TIMEOUT":     "-1s",
		"OPENAI_TIMEOUT":    "soon",
		"PR_NUMBER":         "abc",
		"GITHUB_REPOSITORY": "not-a-repo",
		"OPENAI_BASE_URL":   "http://example.com/v1",
		"GITHUB_API_URL":    "api.github.com",
		"LOG_LEVEL":         "verbose",
		"LOG_FORMAT":        "xml",
		"DRY_RUN":           "maybe",
	}
	for key, value := range tests {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			env := validEnv()
			env[key] = value
			_, err := FromLookup(lookupFrom(env))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected error mentioning %s, got %v", key, err)
			}
		})
	}
}

func TestFromLookupMaxStepsUpperBound(t *testing.T) {
	t.Parallel()

	env := validEnv()
	env["MAX_AGENT_STEPS"] = "1000"
	if _, err := FromLookup(lookupFrom(env)); err == nil {
		t.Fatal("expected MAX_AGENT_STEPS above the hard limit to be rejected")
	}
}

func TestFromLookupRejectsPlaceholders(t *testing.T) {
	t.Parallel()

	env := validEnv()
	env["OPENAI_API_KEY"] = "your_openai_api_key"
	_, err := FromLookup(lookupFrom(env))
	if err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("expected placeholder error, got %v", err)
	}
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	t.Parallel()

	env := validEnv()
	env["OPENAI_BASE_URL"] = "https://user:" + testGitHubToken + "@example.com"
	env["PR_NUMBER"] = "x"
	_, err := FromLookup(lookupFrom(env))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, s := range []string{testOpenAIKey, testGitHubToken} {
		if strings.Contains(err.Error(), s) {
			t.Fatalf("error leaks a secret: %v", err)
		}
	}
}

func TestSecretNeverFormatsRawValue(t *testing.T) {
	t.Parallel()

	cfg, err := FromLookup(lookupFrom(validEnv()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jsonBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var jsonLog, textLog bytes.Buffer
	slog.New(slog.NewJSONHandler(&jsonLog, nil)).Info("cfg", "config", cfg, "raw", *cfg)
	slog.New(slog.NewTextHandler(&textLog, nil)).Info("cfg", "config", cfg, "raw", *cfg)

	outputs := map[string]string{
		"%v":        fmt.Sprintf("%v", cfg),
		"%+v":       fmt.Sprintf("%+v", cfg),
		"%#v":       fmt.Sprintf("%#v", cfg),
		"%s":        fmt.Sprintf("%s", cfg.OpenAI.APIKey),
		"%q":        fmt.Sprintf("%q", cfg.OpenAI.APIKey),
		"%d":        fmt.Sprintf("%d", cfg.OpenAI.APIKey), //nolint:staticcheck // deliberately wrong verb
		"%x":        fmt.Sprintf("%x", cfg.GitHub.Token),
		"json":      string(jsonBytes),
		"slog json": jsonLog.String(),
		"slog text": textLog.String(),
	}
	for name, out := range outputs {
		for _, s := range cfg.Secrets() {
			if strings.Contains(out, s) {
				t.Errorf("%s output leaks a secret: %s", name, out)
			}
		}
		if !strings.Contains(out, redacted) {
			t.Errorf("%s output has no mask: %s", name, out)
		}
	}
}

func TestParseDotEnv(t *testing.T) {
	t.Parallel()

	input := `
# comment
PLAIN=value
export EXPORTED=yes
SPACED = padded
DOUBLE="has # hash"
SINGLE='single quoted'
INLINE=value # trailing comment
EMPTY=
`
	got, err := parseDotEnv(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"PLAIN": "value", "EXPORTED": "yes", "SPACED": "padded", "DOUBLE": "has # hash",
		"SINGLE": "single quoted", "INLINE": "value", "EMPTY": "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseDotEnvErrorOmitsLineContent(t *testing.T) {
	t.Parallel()

	_, err := parseDotEnv(strings.NewReader("OK=1\n" + testOpenAIKey + "\n"))
	if err == nil {
		t.Fatal("expected error for malformed line")
	}
	if !strings.Contains(err.Error(), "line 2") || strings.Contains(err.Error(), testOpenAIKey) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// The tests below mutate the process environment, so they cannot run in parallel.

// unsetenv removes key for the duration of the test; t.Setenv registers the
// cleanup that restores the original value.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
}

func TestLoadEnvFileDoesNotOverrideEnvironment(t *testing.T) {
	for k, v := range validEnv() {
		t.Setenv(k, v)
	}
	unsetenv(t, "GITHUB_ACTIONS")
	unsetenv(t, "MIN_CONFIDENCE")
	t.Setenv("MAX_AGENT_STEPS", "7")

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("MAX_AGENT_STEPS=3\nMIN_CONFIDENCE=0.8\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agent.MaxSteps != 7 {
		t.Errorf("MaxSteps = %d, want environment value 7", cfg.Agent.MaxSteps)
	}
	if cfg.Agent.MinConfidence != 0.8 {
		t.Errorf("MinConfidence = %v, want file value 0.8", cfg.Agent.MinConfidence)
	}
	if _, set := os.LookupEnv("MIN_CONFIDENCE"); set {
		t.Error("env file values must not be exported to the process environment")
	}
}

func TestLoadRefusesEnvFileInGitHubActions(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "true")

	_, err := Load(filepath.Join(t.TempDir(), ".env"))
	if err == nil || !strings.Contains(err.Error(), "GitHub Actions") {
		t.Fatalf("expected refusal, got %v", err)
	}
}
