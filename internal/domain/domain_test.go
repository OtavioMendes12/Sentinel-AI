package domain

import (
	"math"
	"strings"
	"testing"
)

func validFinding() Finding {
	return Finding{
		Severity:    SeverityHigh,
		Confidence:  0.9,
		Category:    CategoryErrorHandling,
		File:        "internal/user/service.go",
		Line:        87,
		Title:       "Error from repository.Save is ignored",
		Explanation: "A failed save is reported to the caller as success.",
		Evidence:    "_ = s.repo.Save(ctx, user)",
		Suggestion:  "Return the error wrapped with context.",
	}
}

func TestFindingValidateAcceptsValid(t *testing.T) {
	t.Parallel()

	if err := validFinding().Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	f := validFinding()
	f.Suggestion = ""
	if err := f.Validate(); err != nil {
		t.Fatalf("suggestion should be optional: %v", err)
	}
}

func TestFindingValidateRejects(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		mutate func(*Finding)
		field  string
	}{
		"unknown severity":      {func(f *Finding) { f.Severity = "blocker" }, "severity"},
		"style category":        {func(f *Finding) { f.Category = "style" }, "category"},
		"confidence above 1":    {func(f *Finding) { f.Confidence = 1.2 }, "confidence"},
		"negative confidence":   {func(f *Finding) { f.Confidence = -0.1 }, "confidence"},
		"NaN confidence":        {func(f *Finding) { f.Confidence = math.NaN() }, "confidence"},
		"missing file":          {func(f *Finding) { f.File = "" }, "file"},
		"absolute path":         {func(f *Finding) { f.File = "/etc/passwd" }, "file"},
		"parent traversal":      {func(f *Finding) { f.File = "../secrets.txt" }, "file"},
		"inner traversal":       {func(f *Finding) { f.File = "a/../../b.go" }, "file"},
		"dot prefix":            {func(f *Finding) { f.File = "./main.go" }, "file"},
		"backslashes":           {func(f *Finding) { f.File = `internal\user.go` }, "file"},
		"control chars":         {func(f *Finding) { f.File = "main.go\n" }, "file"},
		"zero line":             {func(f *Finding) { f.Line = 0 }, "line"},
		"blank title":           {func(f *Finding) { f.Title = "  " }, "title"},
		"long title":            {func(f *Finding) { f.Title = strings.Repeat("x", MaxTitleLen+1) }, "title"},
		"missing explanation":   {func(f *Finding) { f.Explanation = "" }, "explanation"},
		"missing evidence":      {func(f *Finding) { f.Evidence = "" }, "evidence"},
		"oversized suggestion":  {func(f *Finding) { f.Suggestion = strings.Repeat("x", MaxTextLen+1) }, "suggestion"},
		"multibyte at the edge": {func(f *Finding) { f.Title = strings.Repeat("é", MaxTitleLen+1) }, "title"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := validFinding()
			tt.mutate(&f)
			err := f.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.field+":") {
				t.Fatalf("expected error on %s, got %v", tt.field, err)
			}
		})
	}
}

func TestFindingValidateCountsRunesNotBytes(t *testing.T) {
	t.Parallel()

	f := validFinding()
	f.Title = strings.Repeat("é", MaxTitleLen) // 240 bytes, 120 characters
	if err := f.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReviewValidate(t *testing.T) {
	t.Parallel()

	ok := Review{Summary: "One relevant problem found.", Findings: []Finding{validFinding()}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := (Review{Summary: "No problems found."}).Validate(); err != nil {
		t.Fatalf("an empty findings list must be valid: %v", err)
	}

	bad := validFinding()
	bad.Severity = "urgent"
	bad.Line = -1
	err := Review{Summary: " ", Findings: []Finding{validFinding(), bad}}.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"summary:", "findings[1].severity:", "findings[1].line:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "findings[0]") {
		t.Errorf("valid finding reported as invalid: %v", err)
	}
}

func TestReviewValidateLimitsFindings(t *testing.T) {
	t.Parallel()

	r := Review{Summary: "Too many.", Findings: make([]Finding, MaxFindings+1)}
	for i := range r.Findings {
		r.Findings[i] = validFinding()
	}
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("expected findings limit error, got %v", err)
	}
}

func TestWithoutInvalidFindings(t *testing.T) {
	t.Parallel()

	bad := validFinding()
	bad.Evidence = ""
	got, dropped := Review{Summary: "s", Findings: []Finding{bad, validFinding(), bad}}.WithoutInvalidFindings()
	if dropped != 2 || len(got.Findings) != 1 || got.Summary != "s" {
		t.Fatalf("got %d findings, %d dropped", len(got.Findings), dropped)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("result must be valid: %v", err)
	}
}

func TestPartition(t *testing.T) {
	t.Parallel()

	finding := func(sev Severity, conf float64, file string, line int) Finding {
		f := validFinding()
		f.Severity, f.Confidence, f.File, f.Line = sev, conf, file, line
		return f
	}
	review := Review{Summary: "s", Findings: []Finding{
		finding(SeverityLow, 0.95, "a.go", 1),
		finding(SeverityCritical, 0.80, "b.go", 2),
		finding(SeverityHigh, 0.60, "c.go", 3), // below threshold
		finding(SeverityHigh, 0.75, "d.go", 4), // exactly at threshold
		finding(SeverityCritical, 0.90, "e.go", 5),
		finding(SeverityCritical, 0.70, "b.go", 2), // duplicate of b.go:2, less confident
	}}

	publish, suppress := review.Partition(0.75)

	var got []string
	for _, f := range publish {
		got = append(got, f.File)
	}
	if want := "e.go,b.go,d.go,a.go"; strings.Join(got, ",") != want {
		t.Errorf("publish order = %v, want %s", got, want)
	}
	if publish[1].Confidence != 0.80 {
		t.Errorf("duplicate should keep the most confident finding, got %v", publish[1].Confidence)
	}
	if len(suppress) != 1 || suppress[0].File != "c.go" {
		t.Errorf("suppress = %+v, want only c.go", suppress)
	}
}
