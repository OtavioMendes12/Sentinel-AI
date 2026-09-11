// Package config loads and validates runtime configuration from environment
// variables. Defaults exist only for non-sensitive values; missing credentials
// fail startup. Secrets are wrapped in Secret so they cannot be printed.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOpenAIModel   = "gpt-4.1"
	defaultOpenAIBaseURL = "https://api.openai.com/v1"
	defaultOpenAITimeout = 60 * time.Second
	defaultGitHubAPIURL  = "https://api.github.com"
	defaultMinConfidence = 0.75
	defaultMaxAgentSteps = 10
	maxAgentStepsLimit   = 50
	defaultAgentTimeout  = 5 * time.Minute
	defaultRepoPath      = "."
	defaultLogFormat     = "json"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Config is the complete runtime configuration of the reviewer.
type Config struct {
	OpenAI   OpenAIConfig
	GitHub   GitHubConfig
	Agent    AgentConfig
	Log      LogConfig
	RepoPath string
	DryRun   bool
}

// OpenAIConfig configures the LLM provider.
type OpenAIConfig struct {
	APIKey  Secret
	Model   string
	BaseURL string
	Timeout time.Duration
}

// GitHubConfig configures GitHub access and the pull request under review.
type GitHubConfig struct {
	Token      Secret
	APIURL     string
	Repository string // "owner/name"
	PRNumber   int
}

// AgentConfig bounds the agent loop.
type AgentConfig struct {
	MaxSteps      int
	MinConfidence float64
	Timeout       time.Duration
}

// LogConfig configures structured logging.
type LogConfig struct {
	Level  slog.Level
	Format string // "json" or "text"
}

// Load reads configuration from the process environment. When envFile is set,
// variables missing from the environment are read from that file; the real
// environment always wins.
//
// Env files are refused inside GitHub Actions: the working directory there is
// the pull request checkout, so a .env file is attacker-controlled and could,
// for example, point OPENAI_BASE_URL at a server that harvests the API key.
func Load(envFile string) (*Config, error) {
	lookup := os.LookupEnv
	if envFile != "" {
		if v, _ := os.LookupEnv("GITHUB_ACTIONS"); v == "true" {
			return nil, errors.New("refusing to load an env file inside GitHub Actions; use repository secrets")
		}
		fileVars, err := readDotEnv(envFile)
		if err != nil {
			return nil, err
		}
		lookup = func(key string) (string, bool) {
			if v, ok := os.LookupEnv(key); ok {
				return v, true
			}
			v, ok := fileVars[key]
			return v, ok
		}
	}
	return FromLookup(lookup)
}

// FromLookup builds a Config from an arbitrary variable source. All problems
// are collected and returned together so a misconfigured environment can be
// fixed in one pass. Error messages never include secret values.
func FromLookup(lookup func(string) (string, bool)) (*Config, error) {
	p := &parser{lookup: lookup}
	cfg := &Config{
		OpenAI: OpenAIConfig{
			APIKey:  p.secret("OPENAI_API_KEY"),
			Model:   p.str("OPENAI_MODEL", defaultOpenAIModel),
			BaseURL: p.url("OPENAI_BASE_URL", defaultOpenAIBaseURL),
			Timeout: p.duration("OPENAI_TIMEOUT", defaultOpenAITimeout),
		},
		GitHub: GitHubConfig{
			Token:      p.secret("GITHUB_TOKEN"),
			APIURL:     p.url("GITHUB_API_URL", defaultGitHubAPIURL),
			Repository: p.repository("GITHUB_REPOSITORY"),
			PRNumber:   p.positiveInt("PR_NUMBER"),
		},
		Agent: AgentConfig{
			MaxSteps:      p.intInRange("MAX_AGENT_STEPS", defaultMaxAgentSteps, 1, maxAgentStepsLimit),
			MinConfidence: p.probability("MIN_CONFIDENCE", defaultMinConfidence),
			Timeout:       p.duration("AGENT_TIMEOUT", defaultAgentTimeout),
		},
		Log: LogConfig{
			Level:  p.logLevel("LOG_LEVEL"),
			Format: p.oneOf("LOG_FORMAT", defaultLogFormat, "json", "text"),
		},
		RepoPath: p.str("REPO_PATH", defaultRepoPath),
		DryRun:   p.boolean("DRY_RUN"),
	}
	if err := errors.Join(p.errs...); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Secrets returns the raw values of every configured credential, for use by
// a redact.Redactor. Never log the result.
func (c *Config) Secrets() []string {
	return []string{c.OpenAI.APIKey.Reveal(), c.GitHub.Token.Reveal()}
}

// LogValue implements slog.LogValuer. Secrets render as "***", which still
// shows whether they are set.
func (c *Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Group("openai",
			slog.Any("api_key", c.OpenAI.APIKey),
			slog.String("model", c.OpenAI.Model),
			slog.String("base_url", c.OpenAI.BaseURL),
			slog.Duration("timeout", c.OpenAI.Timeout),
		),
		slog.Group("github",
			slog.Any("token", c.GitHub.Token),
			slog.String("api_url", c.GitHub.APIURL),
			slog.String("repository", c.GitHub.Repository),
			slog.Int("pr_number", c.GitHub.PRNumber),
		),
		slog.Group("agent",
			slog.Int("max_steps", c.Agent.MaxSteps),
			slog.Float64("min_confidence", c.Agent.MinConfidence),
			slog.Duration("timeout", c.Agent.Timeout),
		),
		slog.String("log_level", c.Log.Level.String()),
		slog.String("repo_path", c.RepoPath),
		slog.Bool("dry_run", c.DryRun),
	)
}

// parser reads typed values and accumulates validation errors.
type parser struct {
	lookup func(string) (string, bool)
	errs   []error
}

func (p *parser) fail(format string, args ...any) {
	p.errs = append(p.errs, fmt.Errorf(format, args...))
}

// value returns the trimmed value; set-but-empty counts as unset, which is
// what "KEY=" in a .env file means.
func (p *parser) value(key string) (string, bool) {
	v, _ := p.lookup(key)
	v = strings.TrimSpace(v)
	return v, v != ""
}

func (p *parser) secret(key string) Secret {
	v, ok := p.value(key)
	if !ok {
		p.fail("%s is required", key)
		return ""
	}
	if isPlaceholder(v) {
		p.fail("%s still holds a placeholder value; set a real credential", key)
		return ""
	}
	return Secret(v)
}

func isPlaceholder(v string) bool {
	v = strings.ToLower(v)
	return strings.HasPrefix(v, "your_") || v == "change_me" || v == "changeme"
}

func (p *parser) str(key, def string) string {
	if v, ok := p.value(key); ok {
		return v
	}
	return def
}

func (p *parser) oneOf(key, def string, allowed ...string) string {
	v := strings.ToLower(p.str(key, def))
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	p.fail("%s must be one of %s, got %q", key, strings.Join(allowed, ", "), v)
	return def
}

func (p *parser) repository(key string) string {
	v, ok := p.value(key)
	switch {
	case !ok:
		p.fail("%s is required (format: owner/name)", key)
	case !repositoryPattern.MatchString(v):
		p.fail("%s must have the form owner/name, got %q", key, v)
	}
	return v
}

func (p *parser) positiveInt(key string) int {
	v, ok := p.value(key)
	if !ok {
		p.fail("%s is required", key)
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		p.fail("%s must be a positive integer, got %q", key, v)
		return 0
	}
	return n
}

func (p *parser) intInRange(key string, def, lo, hi int) int {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		p.fail("%s must be an integer between %d and %d, got %q", key, lo, hi, v)
		return def
	}
	return n
}

func (p *parser) probability(key string, def float64) float64 {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 || f > 1 {
		p.fail("%s must be a number between 0 and 1, got %q", key, v)
		return def
	}
	return f
}

func (p *parser) duration(key string, def time.Duration) time.Duration {
	v, ok := p.value(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		p.fail("%s must be a positive duration such as 30s or 5m, got %q", key, v)
		return def
	}
	return d
}

func (p *parser) boolean(key string) bool {
	v, ok := p.value(key)
	if !ok {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		p.fail("%s must be true or false, got %q", key, v)
		return false
	}
	return b
}

func (p *parser) logLevel(key string) slog.Level {
	var level slog.Level
	v, ok := p.value(key)
	if !ok {
		return slog.LevelInfo
	}
	if err := level.UnmarshalText([]byte(v)); err != nil {
		p.fail("%s must be one of debug, info, warn, error, got %q", key, v)
		return slog.LevelInfo
	}
	return level
}

// url validates an API endpoint. Credentials are only ever sent over HTTPS;
// plain HTTP is accepted for loopback hosts so tests can use local fakes.
// The raw value is not echoed because a malformed URL may embed credentials.
func (p *parser) url(key, def string) string {
	raw := p.str(key, def)
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "":
		p.fail("%s must be an absolute URL", key)
	case u.User != nil:
		p.fail("%s must not embed credentials", key)
	case u.Scheme == "https":
	case u.Scheme == "http" && isLoopback(u.Hostname()):
	default:
		p.fail("%s must use https (http is allowed only for localhost)", key)
	}
	return strings.TrimRight(raw, "/")
}

func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
