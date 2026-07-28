package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func registryToolDefinitions() []okruntime.ToolDefinition {
	specs := core.BuiltinToolSpecs()
	definitions := make([]okruntime.ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		definitions = append(definitions, okruntime.ToolDefinition{
			Name: spec.Name, Description: spec.Description,
			Parameters: spec.Parameters, Sequential: spec.Sequential,
		})
	}
	return definitions
}

func TestProviderNeutralRegistryOwnsWorkspaceWriteSchema(t *testing.T) {
	for _, tool := range core.BuiltinToolSpecs() {
		if tool.Name != core.ToolWorkspaceWriteFile {
			continue
		}
		properties, ok := tool.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatal("workspace write properties missing")
		}
		content, ok := properties["content"].(map[string]any)
		if !ok || content["maxLength"] != 1048576 {
			t.Fatalf("workspace write content schema=%#v", content)
		}
		return
	}
	t.Fatal("workspace write tool missing")
}

func TestProviderErrorBodyIsRedacted(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"SECRET_SHOULD_NOT_LEAK"}`))
	}))
	defer provider.Close()

	client := New(provider.URL, "model-test", func() string { return "fake-test-key" })
	result, err := client.Complete(context.Background(), okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err == nil || result.ErrorCode != "PROVIDER_AUTH" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.StopReason != core.StopReasonError {
		t.Fatalf("stop reason=%q", result.StopReason)
	}
	if strings.Contains(err.Error(), "SECRET_SHOULD_NOT_LEAK") {
		t.Fatalf("provider response leaked through error: %v", err)
	}
}

func TestWorkspaceToolCallsAreStructuredAndBounded(t *testing.T) {
	t.Parallel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"model-test" ,"tool_calling":true}]}`))
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tools, ok := request["tools"].([]any)
		if !ok || len(tools) != len(core.BuiltinToolSpecs()) || request["tool_choice"] != "auto" {
			t.Errorf("unexpected tool contract: %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "provider-tool-call",
			"choices": []map[string]any{{"message": map[string]any{
				"role": "assistant", "content": nil,
				"tool_calls": []map[string]any{{"id": "provider-id", "type": "function", "function": map[string]any{"name": core.ToolWorkspaceWriteFile, "arguments": `{"path":"result.md","content":"done"}`}}},
			}}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	defer provider.Close()

	client := New(provider.URL, "model-test", func() string { return "fake-test-key" })
	result, err := client.Complete(context.Background(), okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user", EnableTools: true, ToolDefinitions: registryToolDefinitions()})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != core.ToolWorkspaceWriteFile || result.ToolCalls[0].Arguments["path"] != "result.md" {
		t.Fatalf("tool calls=%#v", result.ToolCalls)
	}
	if result.Content == "" {
		t.Fatal("empty tool-only response did not receive a transparent user summary")
	}
}

func TestToolArgumentsCompatibilityIsConservative(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantPath string
		repaired bool
	}{
		{name: "valid", raw: `{"path":"result.md","content":"done"}`, wantPath: "result.md"},
		{name: "object fence", raw: "```json\n{\"path\":\"result.md\",\"content\":\"done\"}\n```", wantPath: "result.md", repaired: true},
		{name: "raw newline", raw: "{\"path\":\"result.md\",\"content\":\"first\nsecond\"}", wantPath: "result.md", repaired: true},
		{name: "invalid escape", raw: `{"path":"result.md","content":"a\q"}`, wantPath: "result.md", repaired: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			arguments, repaired, err := parseToolArguments(test.raw)
			if err != nil || arguments["path"] != test.wantPath || repaired != test.repaired {
				t.Fatalf("arguments=%#v repaired=%v err=%v", arguments, repaired, err)
			}
		})
	}
	if _, _, err := parseToolArguments(`{"path":"result.md","content":"cut`); err == nil {
		t.Fatal("truncated arguments must never be repaired into an executable object")
	}
}

func TestProviderAcceptsObjectToolArguments(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "provider-object-arguments",
			"choices": []map[string]any{{"message": map[string]any{
				"role": "assistant", "content": nil,
				"tool_calls": []map[string]any{{"id": "provider-id", "type": "function", "function": map[string]any{"name": core.ToolWebSearch, "arguments": map[string]any{"query": "Physical AI"}}}},
			}}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 3},
		})
	}))
	defer provider.Close()

	client := New(provider.URL, "model-test", func() string { return "fake-test-key" })
	result, err := client.Complete(t.Context(), okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil || len(result.ToolCalls) != 1 || result.ToolCalls[0].Arguments["query"] != "Physical AI" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestProviderClassifiesTruncatedToolArguments(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "provider-truncated-arguments",
			"choices": []map[string]any{{"message": map[string]any{
				"role": "assistant", "content": nil,
				"tool_calls": []map[string]any{{"id": "provider-id", "type": "function", "function": map[string]any{"name": core.ToolWebSearch, "arguments": `{"query":"Physical AI`}}},
			}}},
			"usage": map[string]any{"prompt_tokens": 4, "completion_tokens": 3},
		})
	}))
	defer provider.Close()

	client := New(provider.URL, "model-test", func() string { return "fake-test-key" })
	result, err := client.Complete(t.Context(), okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err == nil || result.ErrorCode != "PROVIDER_TOOL_ARGUMENTS_TRUNCATED" || result.Usage.InputTokens != 4 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestSelectedSkillConstrainsVisibleBuiltInTools(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"id": "model-test", "tool_calling": true}}})
			return
		}
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Tools) != 1 || request.Tools[0].Function.Name != core.ToolWebSearch {
			t.Fatalf("tools=%#v", request.Tools)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "scoped-tools", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "scoped"}}}, "usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}})
	}))
	defer provider.Close()

	client := New(provider.URL, "model-test", func() string { return "fake-test-key" })
	result, err := client.Complete(t.Context(), okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user", EnableTools: true, ToolDefinitions: registryToolDefinitions(), ToolScopeDeclared: true, AllowedTools: []string{core.ToolWebSearch}})
	if err != nil || result.Content != "scoped" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestToolCapabilityMismatchUsesCompatibleFallback(t *testing.T) {
	var chatCalls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"no-tools","tool_calling":false},{"id":"tools-ok","tool_calling":true}]}`))
			return
		}
		chatCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fallback-call","choices":[{"message":{"role":"assistant","content":"fallback after capability check"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer provider.Close()

	client := New(provider.URL, "", func() string { return "fake-test-key" })
	result, err := client.Complete(context.Background(), okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user", EnableTools: true, ToolDefinitions: registryToolDefinitions(), Model: "no-tools", FallbackModel: "tools-ok"})
	if err != nil || result.Model != "tools-ok" || result.RoutingReason != "fallback" || result.Content == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if chatCalls != 1 {
		t.Fatalf("chat calls=%d; incompatible primary should be blocked before POST", chatCalls)
	}
}

func TestFallbackModelIsUsedForRetryableProviderRejection(t *testing.T) {
	t.Parallel()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request["model"] == "primary-model" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fallback-request", "choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": "fallback used"}}}, "usage": map[string]any{"prompt_tokens": 2, "completion_tokens": 2}})
	}))
	defer provider.Close()

	client := New(provider.URL, "", func() string { return "fake-test-key" })
	result, err := client.Complete(context.Background(), okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user", Model: "primary-model", FallbackModel: "fallback-model"})
	if err != nil || result.Content != "fallback used" || result.Model != "fallback-model" || result.RoutingReason != "fallback" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestListModelsFiltersNonChatModelsAndSorts(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fake-test-key" {
			t.Fatalf("authorization header not forwarded safely")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"embedding-v1"},{"id":"zeta-chat"},{"id":"gpt-4o-mini","tool_calling":true,"vision":true,"context_window":128000},{"id":"whisper-1"}]}`))
	}))
	defer provider.Close()

	client := New(provider.URL, "", func() string { return "fake-test-key" })
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gpt-4o-mini" || models[1].ID != "zeta-chat" {
		t.Fatalf("models=%#v", models)
	}
	if !models[0].ToolCalling || !models[0].Vision || models[0].ContextWindow != 128000 {
		t.Fatalf("model capabilities=%#v", models[0])
	}
}

func TestListModelsRequiresCredential(t *testing.T) {
	client := New("http://127.0.0.1:1", "", func() string { return "" })
	_, err := client.ListModels(context.Background())
	if err == nil || !strings.Contains(err.Error(), "MODEL_CREDENTIAL_MISSING") {
		t.Fatalf("err=%v", err)
	}
}

func TestConfiguredModelRemainsSelectableWhenCatalogIsUnavailable(t *testing.T) {
	client := New("http://127.0.0.1:1", "kimi-for-coding", func() string { return "" })
	models, err := client.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "kimi-for-coding" {
		t.Fatalf("models=%#v err=%v", models, err)
	}
	model, err := client.DefaultModel(context.Background())
	if err != nil || model != "kimi-for-coding" {
		t.Fatalf("default model=%q err=%v", model, err)
	}
}

func TestStreamEmitsProviderDeltasAndReturnsFinalResult(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["stream"] != true {
			t.Fatalf("stream=%#v", request["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"stream-1\",\"choices\":[{\"delta\":{\"content\":\"第一段\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"stream-1\",\"choices\":[{\"delta\":{\"content\":\"第二段\"}}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()

	client := New(provider.URL, "model-stream", func() string { return "fake-test-key" })
	var deltas []string
	result, err := client.Stream(t.Context(), okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user"}, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "") != "第一段第二段" || result.Content != "第一段第二段" {
		t.Fatalf("deltas=%#v result=%#v", deltas, result)
	}
	if result.Usage.InputTokens != 9 || result.Usage.OutputTokens != 4 {
		t.Fatalf("usage=%#v", result.Usage)
	}
}

func TestTypedMessagesMapToolImageThinkingStopReasonAndUsageV2(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Messages) != 4 || request.Messages[0].Role != "system" || request.Messages[1].Role != "user" || request.Messages[2].Role != "assistant" || request.Messages[3].Role != "tool" {
			t.Fatalf("messages=%#v", request.Messages)
		}
		userParts, ok := request.Messages[1].Content.([]any)
		if !ok || len(userParts) != 2 {
			t.Fatalf("user content=%#v", request.Messages[1].Content)
		}
		if len(request.Messages[2].ToolCalls) != 1 || request.Messages[2].ToolCalls[0].Function.Name != "read_file" {
			t.Fatalf("assistant tool calls=%#v", request.Messages[2].ToolCalls)
		}
		if request.Messages[3].ToolCallID != "call-previous" || request.Messages[3].Content != "file body" {
			t.Fatalf("tool result=%#v", request.Messages[3])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "typed-response",
			"choices": []map[string]any{{
				"finish_reason": "tool_calls",
				"message": map[string]any{
					"role":              "assistant",
					"content":           "checking",
					"reasoning_content": "private reasoning",
					"tool_calls": []map[string]any{{
						"id": "call-next", "type": "function",
						"function": map[string]any{"name": "read_file", "arguments": `{"path":"README.md"}`},
					}},
				},
			}},
			"usage": map[string]any{
				"prompt_tokens": 10, "completion_tokens": 5,
				"prompt_tokens_details":     map[string]any{"cached_tokens": 4},
				"completion_tokens_details": map[string]any{"reasoning_tokens": 2},
			},
		})
	}))
	defer provider.Close()

	client := New(provider.URL, "model-typed", func() string { return "fake-test-key" })
	result, err := client.Complete(t.Context(), okruntime.ModelRequest{
		SystemPrompt:   "system",
		RequestedModel: core.ModelRef{ProviderID: "provider-instance-1", ModelID: "model-typed"},
		Messages: []core.ModelMessage{
			{Role: core.MessageRoleUser, Content: []core.ContentBlock{
				{Type: core.ContentBlockText, Text: "inspect"},
				{Type: core.ContentBlockImage, Image: &core.ImageBlock{URL: "https://example.invalid/image.png", MediaType: "image/png"}},
			}},
			{Role: core.MessageRoleAssistant, Content: []core.ContentBlock{{Type: core.ContentBlockToolCall, ToolCall: &core.ToolCallBlock{ID: "call-previous", Name: "read_file", Arguments: map[string]any{"path": "README.md"}}}}},
			{Role: core.MessageRoleTool, Content: []core.ContentBlock{{Type: core.ContentBlockToolResult, ToolResult: &core.ToolResultBlock{ToolCallID: "call-previous", ToolName: "read_file", Content: "file body"}}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != core.StopReasonToolUse || len(result.ContentBlocks) != 3 || result.ContentBlocks[1].Type != core.ContentBlockThinking || result.ContentBlocks[2].ToolCall == nil {
		t.Fatalf("result blocks=%#v stop=%q", result.ContentBlocks, result.StopReason)
	}
	if result.RequestedModel.ProviderID != "provider-instance-1" || result.EffectiveModel.ModelID != "model-typed" {
		t.Fatalf("model refs requested=%#v effective=%#v", result.RequestedModel, result.EffectiveModel)
	}
	if result.Usage.InputTokens != 6 || result.Usage.ContextTokens != 10 || result.Usage.CacheReadTokens != 4 || result.Usage.ReasoningTokens != 2 || result.Usage.UsageSemanticsVersion != "v2" {
		t.Fatalf("usage=%#v", result.Usage)
	}
}

func TestLengthFinishReasonIsPreserved(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"partial"}}],"usage":{"prompt_tokens":2,"completion_tokens":1}}`))
	}))
	defer provider.Close()
	client := New(provider.URL, "model-length", func() string { return "fake-test-key" })
	result, err := client.Complete(t.Context(), okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err != nil || result.StopReason != core.StopReasonLength || result.Content != "partial" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestStreamReassemblesTypedToolCall(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"stream-tools\",\"choices\":[{\"delta\":{\"reasoning_content\":\"inspect path\",\"tool_calls\":[{\"index\":0,\"id\":\"call-stream\",\"type\":\"function\",\"function\":{\"name\":\"read_\",\"arguments\":\"{\\\"path\\\":\\\"\"}}]}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"stream-tools\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"file\",\"arguments\":\"README.md\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()
	client := New(provider.URL, "model-stream-tools", func() string { return "fake-test-key" })
	result, err := client.Stream(t.Context(), okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user"}, nil)
	if err != nil || result.StopReason != core.StopReasonToolUse || len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "read_file" || result.ToolCalls[0].Arguments["path"] != "README.md" || len(result.ContentBlocks) != 2 || result.ContentBlocks[0].Type != core.ContentBlockThinking {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestCanceledRequestUsesAbortedStopReason(t *testing.T) {
	client := New("http://127.0.0.1:1", "model-canceled", func() string { return "fake-test-key" })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := client.Complete(ctx, okruntime.ModelRequest{SystemPrompt: "system", UserPrompt: "user"})
	if err == nil || result.StopReason != core.StopReasonAborted {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
