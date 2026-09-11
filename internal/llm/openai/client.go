// Package openai adapts the OpenAI Chat Completions API to llm.Client. Any
// OpenAI-compatible endpoint works through OPENAI_BASE_URL.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/OtavioMendes12/Sentinel-AI/internal/httpx"
	"github.com/OtavioMendes12/Sentinel-AI/internal/llm"
)

const maxResponseBytes = 4 << 20

// Client implements llm.Client.
type Client struct {
	baseURL string
	apiKey  string
	model   string
	http    *http.Client
	retry   httpx.RetryPolicy
}

// New returns a client for the given model. httpClient should carry the
// per-request timeout.
func New(baseURL, apiKey, model string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		http:    httpClient,
		retry:   httpx.DefaultRetryPolicy,
	}
}

// Wire types for POST /chat/completions.
type (
	chatRequest struct {
		Model      string        `json:"model"`
		Messages   []chatMessage `json:"messages"`
		Tools      []chatTool    `json:"tools,omitempty"`
		ToolChoice string        `json:"tool_choice,omitempty"`
	}
	chatMessage struct {
		Role       string         `json:"role"`
		Content    *string        `json:"content"`
		ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
		ToolCallID string         `json:"tool_call_id,omitempty"`
	}
	chatToolCall struct {
		ID       string       `json:"id"`
		Type     string       `json:"type"`
		Function chatFunction `json:"function"`
	}
	chatFunction struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON encoded as a string
	}
	chatTool struct {
		Type     string          `json:"type"`
		Function chatToolFuncDef `json:"function"`
	}
	chatToolFuncDef struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}
	chatResponse struct {
		Choices []struct {
			Message      chatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	errorResponse struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
)

// Complete sends one chat completion request.
func (c *Client) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	body, err := json.Marshal(c.toWire(req))
	if err != nil {
		return nil, fmt.Errorf("openai: encoding request: %w", err)
	}

	resp, err := httpx.Do(ctx, c.http, c.retry, func(ctx context.Context) (*http.Request, error) {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+c.apiKey)
		return r, nil
	})
	if err != nil {
		return nil, fmt.Errorf("openai: %w", describe(err))
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := httpx.ReadLimited(resp.Body, maxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("openai: reading response: %w", err)
	}
	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("openai: decoding response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("openai: response has no choices")
	}
	return fromWire(out), nil
}

func (c *Client) toWire(req llm.Request) chatRequest {
	wire := chatRequest{Model: c.model, Messages: make([]chatMessage, 0, len(req.Messages))}
	for _, m := range req.Messages {
		msg := chatMessage{Role: string(m.Role), ToolCallID: m.ToolCallID}
		// Assistant messages that only carry tool calls have null content.
		if m.Content != "" || len(m.ToolCalls) == 0 {
			content := m.Content
			msg.Content = &content
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, chatToolCall{
				ID: tc.ID, Type: "function",
				Function: chatFunction{Name: tc.Name, Arguments: string(tc.Arguments)},
			})
		}
		wire.Messages = append(wire.Messages, msg)
	}
	for _, t := range req.Tools {
		wire.Tools = append(wire.Tools, chatTool{Type: "function", Function: chatToolFuncDef{
			Name: t.Name, Description: t.Description, Parameters: t.Parameters,
		}})
	}
	if len(req.Tools) > 0 {
		wire.ToolChoice = "auto"
		if req.RequireToolCall {
			wire.ToolChoice = "required"
		}
	}
	return wire
}

func fromWire(out chatResponse) *llm.Response {
	choice := out.Choices[0]
	msg := llm.Message{Role: llm.RoleAssistant}
	if choice.Message.Content != nil {
		msg.Content = *choice.Message.Content
	}
	for _, tc := range choice.Message.ToolCalls {
		// Arguments are passed through as-is; the orchestrator validates them
		// and reports malformed JSON back to the model.
		msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
			ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments),
		})
	}
	return &llm.Response{
		Message: msg,
		Usage:   llm.Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens},
	}
}

// describe turns an HTTP status error into the provider's error message,
// which is more useful than the raw JSON body.
func describe(err error) error {
	var statusErr *httpx.StatusError
	if !errors.As(err, &statusErr) {
		return err
	}
	var e errorResponse
	if json.Unmarshal([]byte(statusErr.Body), &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("HTTP %d: %s (%s)", statusErr.StatusCode, e.Error.Message, e.Error.Type)
	}
	return err
}
