package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// readDotEnv parses a .env file. Values are returned as a map instead of being
// exported to the process environment, so they never leak into child
// processes such as test runners.
func readDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path) //nolint:gosec // path is an explicit operator-provided flag
	if err != nil {
		return nil, fmt.Errorf("opening env file: %w", err)
	}
	defer func() { _ = f.Close() }()

	vars, err := parseDotEnv(f)
	if err != nil {
		return nil, fmt.Errorf("parsing env file %s: %w", path, err)
	}
	return vars, nil
}

// parseDotEnv supports KEY=VALUE lines, blank lines, # comments, an optional
// "export " prefix, single/double quoted values and trailing " # comments" on
// unquoted values. Errors report line numbers only, never line content, since
// the content may be a secret.
func parseDotEnv(r io.Reader) (map[string]string, error) {
	vars := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !envKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNo)
		}
		vars[key] = unquote(strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}
	return vars, nil
}

func unquote(v string) string {
	if len(v) >= 2 {
		if q := v[0]; (q == '"' || q == '\'') && v[len(v)-1] == q {
			return v[1 : len(v)-1]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}
