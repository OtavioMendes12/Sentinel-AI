// Package tools contains the tool registry and the tools the agent can use.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// Risk classifies what a tool can affect. The orchestrator only exposes to
// the model tools up to a configured maximum risk.
type Risk int

// Risk levels, from least to most dangerous.
const (
	// RiskRead tools inspect repository or pull request data.
	RiskRead Risk = iota + 1
	// RiskExecute tools run allowlisted project commands inside a sandbox.
	RiskExecute
	// RiskWrite tools cause side effects outside the sandbox, such as
	// publishing a review. They are invoked by code, never by the model.
	RiskWrite
)

func (r Risk) String() string {
	switch r {
	case RiskRead:
		return "read"
	case RiskExecute:
		return "execute"
	case RiskWrite:
		return "write"
	}
	return fmt.Sprintf("Risk(%d)", int(r))
}

// Handler runs a tool. args have already been validated against the tool's
// InputSchema. The returned text is redacted, size-capped and marked as
// untrusted before the model sees it. Errors are shown without the untrusted
// marker, so keep repository content out of error messages.
type Handler func(ctx context.Context, args json.RawMessage) (string, error)

// Tool is a capability the agent can invoke.
type Tool struct {
	Name        string
	Description string
	InputSchema *Schema
	Risk        Risk
	Handler     Handler
}

var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Registry is the closed set of tools available to the agent. It is filled at
// startup by code only: nothing the model or the repository produces can
// add, remove or change a tool. Register is not safe for concurrent use;
// populate the registry before starting a review.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds t, rejecting incomplete or duplicate definitions.
func (r *Registry) Register(t Tool) error {
	if !toolNamePattern.MatchString(t.Name) {
		return fmt.Errorf("tool name %q must match %s", t.Name, toolNamePattern)
	}
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("tool %q is already registered", t.Name)
	}
	var errs []error
	if t.Description == "" {
		errs = append(errs, errors.New("description is required"))
	}
	if t.Risk < RiskRead || t.Risk > RiskWrite {
		errs = append(errs, fmt.Errorf("invalid risk level %d", int(t.Risk)))
	}
	if t.Handler == nil {
		errs = append(errs, errors.New("handler is required"))
	}
	if t.InputSchema == nil || t.InputSchema.Type != TypeObject {
		errs = append(errs, errors.New("input schema must be an object schema"))
	} else if err := t.InputSchema.check("input"); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("tool %q: %w", t.Name, err)
	}
	r.tools[t.Name] = t
	return nil
}

// Lookup returns the tool registered under name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Tools returns every registered tool, ordered by name.
func (r *Registry) Tools() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
