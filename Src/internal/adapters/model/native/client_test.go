package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestNativeClientUsesPhaseTimeoutsWithoutBoundingHealthyStreams(t *testing.T) {
	client := New(ProtocolAnthropic, "http://provider.test", "model-test", func() string { return "fake-test-key" })
	if client.httpClient.Timeout != 0 {
		t.Fatalf("provider total timeout=%s, want disabled", client.httpClient.Timeout)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok || transport.ResponseHeaderTimeout != 15*time.Second {
		t.Fatalf("response header timeout=%v, want 15s first-token bound", transport)
	}
}

func TestAnthropicNativeMapsTypedMessagesToolsUsageAndStopReason(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1/models" {
			if request.Header.Get("x-api-key") != "anthropic-key" {
				t.Error("missing anthropic credential header")
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"data": []map[string]any{{"id": "claude-test"}}})
			return
		}
		if request.URL.Path != "/v1/messages" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("x-api-key") != "anthropic-key" || request.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic headers")
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id": "msg-provider", "stop_reason": "tool_use",
			"content": []map[string]any{
				{"type": "thinking", "text": "private reasoning"},
				{"type": "text", "text": "I will inspect it."},
				{"type": "tool_use", "id": "tool-provider", "name": "read_file", "input": map[string]any{"path": "README.md"}},
			},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 7, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 2},
		})
	}))
	defer server.Close()

	client := New(ProtocolAnthropic, server.URL+"/v1", "claude-test", func() string { return "anthropic-key" })
	result, err := client.Complete(context.Background(), typedNativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != core.StopReasonToolUse || len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "read_file" {
		t.Fatalf("result=%#v", result)
	}
	if result.Usage.InputTokens != 10 || result.Usage.CacheReadTokens != 3 || result.Usage.CacheWriteTokens != 2 || result.Usage.ContextTokens != 13 {
		t.Fatalf("usage=%#v", result.Usage)
	}
	if received["system"] != "system contract" || len(received["tools"].([]any)) != 1 {
		t.Fatalf("payload=%#v", received)
	}
	models, err := client.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "claude-test" {
		t.Fatalf("models=%#v err=%v", models, err)
	}
}

func TestGeminiNativeMapsFunctionCallsAndDoesNotExposeProviderBody(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("key") != "gemini-key" {
			t.Error("missing gemini credential query")
		}
		if strings.HasSuffix(request.URL.Path, "/models") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"models": []map[string]any{{"name": "models/gemini-test", "supportedGenerationMethods": []string{"generateContent"}, "inputTokenLimit": 1000}}})
			return
		}
		if !strings.HasSuffix(request.URL.Path, "/models/gemini-test:generateContent") {
			http.NotFound(writer, request)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"candidates": []map[string]any{{"finishReason": "STOP", "content": map[string]any{"parts": []map[string]any{
				{"text": "visible answer"},
				{"functionCall": map[string]any{"name": "read_file", "args": map[string]any{"path": "README.md"}}},
			}}}},
			"usageMetadata": map[string]any{"promptTokenCount": 12, "candidatesTokenCount": 5, "cachedContentTokenCount": 2, "thoughtsTokenCount": 1},
		})
	}))
	defer server.Close()

	client := New(ProtocolGemini, server.URL+"/v1beta", "gemini-test", func() string { return "gemini-key" })
	result, err := client.Complete(context.Background(), typedNativeRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != core.StopReasonToolUse || result.Content != "visible answer" || len(result.ToolCalls) != 1 {
		t.Fatalf("result=%#v", result)
	}
	if result.Usage.InputTokens != 10 || result.Usage.ContextTokens != 12 || result.Usage.ReasoningTokens != 1 {
		t.Fatalf("usage=%#v", result.Usage)
	}
	if _, ok := received["systemInstruction"]; !ok {
		t.Fatalf("payload=%#v", received)
	}

	failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"echoed super-secret provider detail"}`))
	}))
	defer failing.Close()
	_, err = New(ProtocolGemini, failing.URL, "gemini-test", func() string { return "gemini-key" }).Complete(context.Background(), typedNativeRequest())
	if err == nil || strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "gemini-key") {
		t.Fatalf("provider error was not redacted: %v", err)
	}
}

func TestAnthropicNativeStreamsTextToolAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["stream"] != true || request.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("stream request=%#v accept=%q", payload, request.Header.Get("Accept"))
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`{"type":"message_start","message":{"id":"msg-stream","usage":{"input_tokens":10,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool-stream","name":"read_file"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"README.md\"}"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
			`{"type":"message_stop"}`,
		}
		for _, event := range events {
			_, _ = writer.Write([]byte("data: " + event + "\n\n"))
		}
	}))
	defer server.Close()
	client := New(ProtocolAnthropic, server.URL, "claude-test", func() string { return "anthropic-key" })
	var deltas []string
	result, err := client.Stream(context.Background(), typedNativeRequest(), func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "") != "Hello" || result.Content != "Hello" || result.StopReason != core.StopReasonToolUse {
		t.Fatalf("deltas=%#v result=%#v", deltas, result)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].ID != "tool-stream" || result.ToolCalls[0].Arguments["path"] != "README.md" {
		t.Fatalf("tool calls=%#v", result.ToolCalls)
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 7 || result.Usage.ContextTokens != 12 {
		t.Fatalf("usage=%#v", result.Usage)
	}
}

func TestGeminiNativeStreamsIncrementalTextAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, ":streamGenerateContent") || request.URL.Query().Get("alt") != "sse" {
			t.Errorf("unexpected stream URL %s", request.URL.String())
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"candidates":[{"content":{"parts":[{"text":"Gem"}]}}]}`,
			`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ini"}]}}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5,"cachedContentTokenCount":2,"thoughtsTokenCount":1}}`,
		}
		for _, chunk := range chunks {
			_, _ = writer.Write([]byte("data: " + chunk + "\n\n"))
		}
	}))
	defer server.Close()
	client := New(ProtocolGemini, server.URL, "gemini-test", func() string { return "gemini-key" })
	var deltas []string
	result, err := client.Stream(context.Background(), typedNativeRequest(), func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "") != "Gemini" || result.Content != "Gemini" || result.StopReason != core.StopReasonStop {
		t.Fatalf("deltas=%#v result=%#v", deltas, result)
	}
	if result.Usage.InputTokens != 10 || result.Usage.ContextTokens != 12 || result.Usage.ReasoningTokens != 1 {
		t.Fatalf("usage=%#v", result.Usage)
	}
}

func typedNativeRequest() okruntime.ModelRequest {
	return okruntime.ModelRequest{
		SystemPrompt: "system contract", Model: "",
		RequestedModel:  core.ModelRef{ProviderID: "provider-native", ModelID: ""},
		MaxOutputTokens: 512, EnableTools: true, ToolScopeDeclared: true, AllowedTools: []string{"read_file"},
		Messages: []core.ModelMessage{{
			Role:    core.MessageRoleUser,
			Content: []core.ContentBlock{{Type: core.ContentBlockText, Text: "inspect the repository"}},
		}},
		ToolDefinitions: []okruntime.ToolDefinition{
			{Name: "read_file", Description: "read", Parameters: map[string]any{"type": "object"}},
			{Name: "run_command", Description: "run", Parameters: map[string]any{"type": "object"}},
		},
	}
}
