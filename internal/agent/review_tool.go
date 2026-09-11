package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/OtavioMendes12/Sentinel-AI/internal/domain"
	"github.com/OtavioMendes12/Sentinel-AI/internal/tools"
)

// SubmitReviewTool is the terminal action of the agent loop. The orchestrator
// handles it itself; it is never part of the tool registry.
const SubmitReviewTool = "submit_review"

const submitReviewDescription = "Submit the final review and end the investigation. " +
	"Call it exactly once, after verifying your findings. An empty findings list is valid."

// reviewSchema describes submit_review's arguments to the model. Enums come
// from the domain package so the two cannot drift apart.
func reviewSchema() *tools.Schema {
	text := func(desc string, maxLen int) *tools.Schema {
		return &tools.Schema{Type: tools.TypeString, Description: desc, MaxLength: maxLen}
	}
	finding := &tools.Schema{
		Type: tools.TypeObject,
		Properties: map[string]*tools.Schema{
			"severity":    {Type: tools.TypeString, Enum: enumValues(domain.Severities())},
			"confidence":  {Type: tools.TypeNumber, Minimum: tools.Bound(0), Maximum: tools.Bound(1), Description: "Calibrated probability that the finding is real."},
			"category":    {Type: tools.TypeString, Enum: enumValues(domain.Categories())},
			"file":        text("Path relative to the repository root.", domain.MaxPathLen),
			"line":        {Type: tools.TypeInteger, Minimum: tools.Bound(1), Description: "Line in the new version of the file."},
			"title":       text("One-line statement of the problem.", domain.MaxTitleLen),
			"explanation": text("Why this is a problem and what goes wrong.", domain.MaxTextLen),
			"evidence":    text("The exact code that shows the problem.", domain.MaxTextLen),
			"suggestion":  text("How to fix it. May be empty.", domain.MaxTextLen),
		},
		Required: []string{"severity", "confidence", "category", "file", "line", "title", "explanation", "evidence", "suggestion"},
	}
	return &tools.Schema{
		Type: tools.TypeObject,
		Properties: map[string]*tools.Schema{
			"summary":  text("Short overall assessment of the pull request.", domain.MaxSummaryLen),
			"findings": {Type: tools.TypeArray, MaxItems: domain.MaxFindings, Items: finding},
		},
		Required: []string{"summary", "findings"},
	}
}

func enumValues[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// decodeReview strictly decodes submit_review arguments. Semantic checks are
// left to domain.Review.Validate, the single source of truth for what a
// valid review is.
func decodeReview(args json.RawMessage) (domain.Review, error) {
	var review domain.Review
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&review); err != nil {
		return domain.Review{}, fmt.Errorf("arguments do not match the submit_review schema: %w", err)
	}
	if dec.More() {
		return domain.Review{}, errors.New("arguments must be a single JSON object")
	}
	return review, nil
}
