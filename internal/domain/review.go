package domain

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Review limits.
const (
	MaxSummaryLen = 2000
	MaxFindings   = 25
)

// Review is the structured outcome of a review run.
type Review struct {
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
}

// Validate returns every problem with the review and its findings, or nil.
func (r Review) Validate() error {
	var errs []error
	switch {
	case strings.TrimSpace(r.Summary) == "":
		errs = append(errs, errors.New("summary: is required"))
	case utf8.RuneCountInString(r.Summary) > MaxSummaryLen:
		errs = append(errs, fmt.Errorf("summary: must be at most %d characters", MaxSummaryLen))
	}
	if len(r.Findings) > MaxFindings {
		errs = append(errs, fmt.Errorf("findings: at most %d findings are allowed; keep the most important", MaxFindings))
	}
	for i, f := range r.Findings {
		errs = append(errs, f.problems(fmt.Sprintf("findings[%d].", i))...)
	}
	return errors.Join(errs...)
}

// WithoutInvalidFindings returns a copy of r containing only valid findings,
// plus the number removed. It is a last resort when the model has no budget
// left to fix its output.
func (r Review) WithoutInvalidFindings() (Review, int) {
	valid := make([]Finding, 0, len(r.Findings))
	for _, f := range r.Findings {
		if f.Validate() == nil {
			valid = append(valid, f)
		}
	}
	if len(valid) > MaxFindings {
		valid = valid[:MaxFindings]
	}
	return Review{Summary: r.Summary, Findings: valid}, len(r.Findings) - len(valid)
}

// Partition splits findings into those to publish (confidence >= minConfidence)
// and those to suppress. Findings on the same file, line and category are
// collapsed into the most confident one. Both slices are ordered by severity,
// then confidence, then location.
func (r Review) Partition(minConfidence float64) (publish, suppress []Finding) {
	for _, f := range dedupe(r.Findings) {
		if f.Confidence >= minConfidence {
			publish = append(publish, f)
		} else {
			suppress = append(suppress, f)
		}
	}
	slices.SortStableFunc(publish, compareFindings)
	slices.SortStableFunc(suppress, compareFindings)
	return publish, suppress
}

func dedupe(findings []Finding) []Finding {
	type key struct {
		file     string
		line     int
		category Category
	}
	index := make(map[key]int, len(findings))
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		k := key{f.File, f.Line, f.Category}
		if i, seen := index[k]; seen {
			if f.Confidence > out[i].Confidence {
				out[i] = f
			}
			continue
		}
		index[k] = len(out)
		out = append(out, f)
	}
	return out
}

func compareFindings(a, b Finding) int {
	return cmp.Or(
		cmp.Compare(b.Severity.Rank(), a.Severity.Rank()),
		cmp.Compare(b.Confidence, a.Confidence),
		cmp.Compare(a.File, b.File),
		cmp.Compare(a.Line, b.Line),
	)
}
