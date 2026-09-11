// Package domain holds the review model shared by the agent, the tools and
// the publishers. It has no infrastructure dependencies.
package domain

import (
	"errors"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Severity ranks the impact of a finding.
type Severity string

// Severities, from most to least severe.
const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

var severityRank = map[Severity]int{
	SeverityLow: 1, SeverityMedium: 2, SeverityHigh: 3, SeverityCritical: 4,
}

// Severities returns every valid severity, most severe first.
func Severities() []Severity {
	return []Severity{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow}
}

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool { _, ok := severityRank[s]; return ok }

// Rank orders severities: higher is more severe, 0 means invalid.
func (s Severity) Rank() int { return severityRank[s] }

// Category classifies a finding. The set is closed on purpose: there is no
// category for style, naming or formatting, so the model cannot report them.
type Category string

// Categories of concrete problems the reviewer reports.
const (
	CategoryBug           Category = "bug"
	CategoryRegression    Category = "regression"
	CategorySecurity      Category = "security"
	CategoryConcurrency   Category = "concurrency"
	CategoryResourceLeak  Category = "resource_leak"
	CategoryErrorHandling Category = "error_handling"
	CategoryContract      Category = "contract"
	CategoryPerformance   Category = "performance"
	CategoryTransaction   Category = "transaction"
	CategoryAPIMisuse     Category = "api_misuse"
	CategoryEdgeCase      Category = "edge_case"
	CategoryMissingTests  Category = "missing_tests"
)

var categories = []Category{
	CategoryBug, CategoryRegression, CategorySecurity, CategoryConcurrency,
	CategoryResourceLeak, CategoryErrorHandling, CategoryContract, CategoryPerformance,
	CategoryTransaction, CategoryAPIMisuse, CategoryEdgeCase, CategoryMissingTests,
}

// Categories returns every valid category.
func Categories() []Category { return slices.Clone(categories) }

// Valid reports whether c is a known category.
func (c Category) Valid() bool { return slices.Contains(categories, c) }

// Field limits keep published comments readable and bound how much text the
// model can place on a pull request.
const (
	MaxTitleLen = 120
	MaxTextLen  = 2000 // explanation, evidence, suggestion
	MaxPathLen  = 512
)

// Finding is a single problem reported by the reviewer.
type Finding struct {
	Severity    Severity `json:"severity"`
	Confidence  float64  `json:"confidence"`
	Category    Category `json:"category"`
	File        string   `json:"file"`
	Line        int      `json:"line"`
	Title       string   `json:"title"`
	Explanation string   `json:"explanation"`
	Evidence    string   `json:"evidence"`
	Suggestion  string   `json:"suggestion"`
}

// Validate returns every problem with the finding, or nil. Messages are
// written so the model can correct its output from them.
func (f Finding) Validate() error {
	return errors.Join(f.problems("")...)
}

// problems prefixes each field name with prefix (e.g. "findings[2].").
func (f Finding) problems(prefix string) []error {
	var errs []error
	add := func(field, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s%s: %s", prefix, field, fmt.Sprintf(format, args...)))
	}

	if !f.Severity.Valid() {
		add("severity", "must be one of %s", joinQuoted(Severities()))
	}
	if !f.Category.Valid() {
		add("category", "must be one of %s", joinQuoted(categories))
	}
	if math.IsNaN(f.Confidence) || f.Confidence < 0 || f.Confidence > 1 {
		add("confidence", "must be between 0 and 1")
	}
	if msg := checkPath(f.File); msg != "" {
		add("file", "%s", msg)
	}
	if f.Line < 1 {
		add("line", "must be a line number (1 or greater) in the new version of the file")
	}
	for _, field := range []struct {
		name, value string
		max         int
		required    bool
	}{
		{"title", f.Title, MaxTitleLen, true},
		{"explanation", f.Explanation, MaxTextLen, true},
		{"evidence", f.Evidence, MaxTextLen, true},
		{"suggestion", f.Suggestion, MaxTextLen, false},
	} {
		switch {
		case field.required && strings.TrimSpace(field.value) == "":
			add(field.name, "is required")
		case utf8.RuneCountInString(field.value) > field.max:
			add(field.name, "must be at most %d characters", field.max)
		}
	}
	return errs
}

// checkPath accepts only clean, relative, slash-separated repository paths.
// Anything else could not be anchored to the diff, or points outside the repo.
func checkPath(p string) string {
	switch {
	case p == "":
		return "is required"
	case len(p) > MaxPathLen:
		return fmt.Sprintf("must be at most %d characters", MaxPathLen)
	case strings.ContainsFunc(p, unicode.IsControl):
		return "must not contain control characters"
	case strings.Contains(p, `\`):
		return "must use forward slashes"
	case strings.HasPrefix(p, "/"):
		return "must be relative to the repository root"
	case path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../"):
		return "must be a clean path inside the repository (no ./, ../ or //)"
	}
	return ""
}

func joinQuoted[T ~string](values []T) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, ", ")
}
