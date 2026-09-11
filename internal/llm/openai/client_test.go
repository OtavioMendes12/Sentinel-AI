package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OtavioMendes12/Sentinel-AI/internal/httpx"
	"github.com/OtavioMendes12/Sentinel-AI/internal/llm"
)

const testKey = "test-openai-key-0000"

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := New(srv.URL+"/v1", testKey, "test-model", srv.Client())
	c.retry = httpx.RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond}
	return c
}

func TestCompleteMapsRequestAndResponse(t *testing.T) {
	t.Parallel()

	var got map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+testKey {
			t.Errorf("unexpected request: %s %v", r.URL.Path, r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{
			"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]}}],
			"usage":{"prompt_tokens":120,"completion_tokens":15}}`))
	})

	resp, err := c.Complete(context.Background(), llm.Request{
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "system"},
			{Role: llm.RoleUser, Content: "review"},
			{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_0", Name: "get_diff", Arguments: json.RawMessage(`{}`)}}},
			{Role: llm.RoleTool, ToolCallID: "call_0", Content: "diff"},
		},
		Tools:           []llm.ToolSpec{{Name: "read_file", Description: "Read a file.", Parameters: json.RawMessage(`{"type":"object"}`)}},
		RequireToolCall: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Request mapping.
	if got["model"] != "test-model" || got["tool_choice"] != "required" {
		t.Errorf("model/tool_choice: %v %v", got["model"], got["tool_choice"])
	}
	msgs := got["messages"].([]any)
	assistant := msgs[2].(map[string]any)
	if assistant["content"] != nil {
		t.Errorf("tool-call-only assistant message must have null content, got %v", assistant["content"])
	}
	fn := assistant["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if fn["arguments"] != "{}" {
		t.Errorf("arguments must be sent as a JSON string, got %#v", fn["arguments"])
	}
	if tool := msgs[3].(map[string]any); tool["tool_call_id"] != "call_0" || tool["content"] != "diff" {
		t.Errorf("tool message: %v", tool)
	}
	tools := got["tools"].([]any)[0].(map[string]any)
	if tools["type"] != "function" || tools["function"].(map[string]any)["name"] != "read_file" {
		t.Errorf("tools: %v", tools)
	}

	// Response mapping.
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "read_file" ||
		string(resp.Message.ToolCalls[0].Arguments) != `{"path":"main.go"}` {
		t.Errorf("tool calls: %+v", resp.Message.ToolCalls)
	}
	if resp.Usage.InputTokens != 120 || resp.Usage.OutputTokens != 15 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestCompleteAutoToolChoice(t *testing.T) {
	t.Parallel()

	c := New("https://api.example.com/v1", testKey, "m", nil)
	wire := c.toWire(llm.Request{Tools: []llm.ToolSpec{{Name: "x", Parameters: json.RawMessage(`{}`)}}})
	if wire.ToolChoice != "auto" {
		t.Errorf("tool_choice = %q", wire.ToolChoice)
	}
	if c.toWire(llm.Request{}).ToolChoice != "" {
		t.Error("tool_choice must be omitted without tools")
	}
}

func TestCompleteReportsProviderErrors(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Incorrect API key provided.","type":"invalid_request_error"}}`))
	})
	_, err := c.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401: Incorrect API key provided.") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Fatalf("error leaks the key: %v", err)
	}
}

func TestCompleteRejectsEmptyChoices(t *testing.T) {
	t.Parallel()

	c := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	})
	if _, err := c.Complete(context.Background(), llm.Request{}); err == nil {
		t.Fatal("expected error")
	}
}
