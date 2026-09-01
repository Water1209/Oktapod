package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/oktapod/oktapod/Src/internal/adapters/model/modeltransport"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

const (
	ProtocolAnthropic = "anthropic"
	ProtocolGemini    = "gemini"
)

type Client struct {
	protocol   string
	endpoint   string
	model      string
	key        func() string
	httpClient *http.Client
	firstToken time.Duration
	streamIdle time.Duration
}

type GatewayError struct {
	Code  string
	Cause error
}

func (e *GatewayError) Error() string {
	return e.Code + ": provider request failed; details were redacted"
}
func (e *GatewayError) ErrorCode() string { return e.Code }
func (e *GatewayError) Unwrap() error     { return e.Cause }

func New(protocol, endpoint, model string, key func() string) *Client {
	return NewWithTimeouts(protocol, endpoint, model, key, modeltransport.DefaultTimeouts())
}

func NewWithTimeouts(protocol, endpoint, model string, key func() string, timeouts modeltransport.Timeouts) *Client {
	timeouts = timeouts.Normalized()
	return &Client{
		protocol:   strings.TrimSpace(protocol),
		endpoint:   strings.TrimRight(strings.TrimSpace(endpoint), "/"),
		model:      strings.TrimSpace(model),
		key:        key,
		httpClient: modeltransport.NewHTTPClientWithTimeouts(timeouts),
		firstToken: timeouts.FirstToken,
		streamIdle: timeouts.StreamIdle,
	}
}

func (c *Client) Complete(ctx context.Context, request okruntime.ModelRequest) (core.ModelResult, error) {
	switch c.protocol {
	case ProtocolAnthropic:
		return c.completeAnthropic(ctx, request)
	case ProtocolGemini:
		return c.completeGemini(ctx, request)
	default:
		return core.ModelResult{StopReason: core.StopReasonError, ErrorCode: "PROVIDER_PROTOCOL_UNSUPPORTED"}, &GatewayError{Code: "PROVIDER_PROTOCOL_UNSUPPORTED"}
	}
}

func (c *Client) Stream(ctx context.Context, request okruntime.ModelRequest, onDelta func(string) error) (core.ModelResult, error) {
	switch c.protocol {
	case ProtocolAnthropic:
		return c.streamAnthropic(ctx, request, onDelta)
	case ProtocolGemini:
		return c.streamGemini(ctx, request, onDelta)
	default:
		return core.ModelResult{StopReason: core.StopReasonError, ErrorCode: "PROVIDER_PROTOCOL_UNSUPPORTED"}, &GatewayError{Code: "PROVIDER_PROTOCOL_UNSUPPORTED"}
	}
}

func (c *Client) streamAnthropic(ctx context.Context, request okruntime.ModelRequest, onDelta func(string) error) (core.ModelResult, error) {
	result, model, key, err := c.resultBase(request)
	if err != nil {
		return result, err
	}
	payload := anthropicPayload(request, model)
	payload["stream"] = true
	result.RequestBytes = serializedPayloadBytes(payload)
	response, started, err := c.postAccept(ctx, c.endpoint+"/messages", payload, map[string]string{
		"x-api-key": key, "anthropic-version": "2023-06-01",
	}, "text/event-stream")
	if err != nil {
		result.StopReason, result.ErrorCode = stopForError(err), errorCode(err)
		return result, err
	}
	result.FirstByteLatency = time.Since(started)
	defer response.Body.Close()
	result.ProviderRequestID = firstNonEmpty(response.Header.Get("request-id"), response.Header.Get("x-request-id"))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		discardProviderBody(response.Body)
		result.StopReason, result.ErrorCode = core.StopReasonError, providerErrorCode(response.StatusCode)
		return result, &GatewayError{Code: result.ErrorCode}
	}
	type blockState struct {
		kind, id, name string
		text           strings.Builder
		input          strings.Builder
	}
	blocks := map[int]*blockState{}
	order := make([]int, 0)
	scanErr := modeltransport.ScanLinesWithProgress(ctx, response.Body, io.LimitReader(response.Body, 16<<20), 4<<20, modeltransport.Remaining(c.firstToken, started), c.streamIdle, func(rawLine string) (bool, error) {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "data:") {
			return false, nil
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			return false, nil
		}
		var event struct {
			Type    string `json:"type"`
			Index   int    `json:"index"`
			Message struct {
				ID    string `json:"id"`
				Usage struct {
					InputTokens              int64 `json:"input_tokens"`
					OutputTokens             int64 `json:"output_tokens"`
					CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
					CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
				Text string `json:"text"`
			} `json:"content_block"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
			} `json:"delta"`
			Usage struct {
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			result.StopReason, result.ErrorCode = core.StopReasonError, "PROVIDER_PROTOCOL_ERROR"
			return false, &GatewayError{Code: result.ErrorCode}
		}
		progressed := false
		switch event.Type {
		case "message_start":
			if event.Message.ID != "" {
				result.ProviderRequestID = event.Message.ID
			}
			result.Usage.InputTokens = event.Message.Usage.InputTokens
			result.Usage.OutputTokens = event.Message.Usage.OutputTokens
			result.Usage.CacheReadTokens = event.Message.Usage.CacheReadInputTokens
			result.Usage.CachedTokens = event.Message.Usage.CacheReadInputTokens
			result.Usage.CacheWriteTokens = event.Message.Usage.CacheCreationInputTokens
		case "content_block_start":
			progressed = event.ContentBlock.Text != "" || event.ContentBlock.Name != ""
			if _, exists := blocks[event.Index]; !exists {
				order = append(order, event.Index)
			}
			state := &blockState{kind: event.ContentBlock.Type, id: event.ContentBlock.ID, name: event.ContentBlock.Name}
			state.text.WriteString(event.ContentBlock.Text)
			blocks[event.Index] = state
		case "content_block_delta":
			state := blocks[event.Index]
			if state == nil {
				state = &blockState{}
				blocks[event.Index] = state
				order = append(order, event.Index)
			}
			switch event.Delta.Type {
			case "text_delta":
				progressed = progressed || event.Delta.Text != ""
				state.kind = "text"
				state.text.WriteString(event.Delta.Text)
				if event.Delta.Text != "" && result.FirstSafeLatency == 0 {
					result.FirstSafeLatency = time.Since(started)
				}
				if onDelta != nil && event.Delta.Text != "" {
					if deltaErr := onDelta(event.Delta.Text); deltaErr != nil {
						result.StopReason = core.StopReasonAborted
						return progressed, deltaErr
					}
				}
			case "thinking_delta":
				progressed = progressed || event.Delta.Thinking != ""
				state.kind = "thinking"
				state.text.WriteString(event.Delta.Thinking)
			case "input_json_delta":
				progressed = progressed || event.Delta.PartialJSON != ""
				state.kind = "tool_use"
				state.input.WriteString(event.Delta.PartialJSON)
			}
		case "message_delta":
			result.StopReason = nativeStopReason(event.Delta.StopReason, false)
			result.Usage.OutputTokens = event.Usage.OutputTokens
		}
		return progressed, nil
	})
	if scanErr != nil {
		if ctx.Err() != nil {
			result.StopReason, result.ErrorCode = core.StopReasonAborted, "MODEL_ABORTED"
			return result, ctx.Err()
		}
		var gatewayErr *GatewayError
		if errors.As(scanErr, &gatewayErr) {
			return result, scanErr
		}
		result.StopReason, result.ErrorCode = core.StopReasonError, "PROVIDER_UNAVAILABLE"
		return result, &GatewayError{Code: result.ErrorCode, Cause: scanErr}
	}
	var visible strings.Builder
	for _, index := range order {
		state := blocks[index]
		if state == nil {
			continue
		}
		switch state.kind {
		case "text":
			text := state.text.String()
			visible.WriteString(text)
			result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockText, Text: text})
		case "thinking":
			result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockThinking, Thinking: &core.ThinkingBlock{Text: state.text.String()}})
		case "tool_use":
			arguments := map[string]any{}
			if raw := strings.TrimSpace(state.input.String()); raw != "" && json.Unmarshal([]byte(raw), &arguments) != nil {
				result.StopReason, result.ErrorCode = core.StopReasonError, "PROVIDER_PROTOCOL_ERROR"
				return result, &GatewayError{Code: result.ErrorCode}
			}
			callID := firstNonEmpty(state.id, uuid.NewString())
			call := core.ToolCall{ID: callID, Name: state.name, Arguments: arguments}
			result.ToolCalls = append(result.ToolCalls, call)
			result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockToolCall, ToolCall: &core.ToolCallBlock{ID: call.ID, Name: call.Name, Arguments: call.Arguments}})
		}
	}
	result.Content = strings.TrimSpace(visible.String())
	if len(result.ToolCalls) > 0 {
		result.StopReason = core.StopReasonToolUse
	} else if result.StopReason == "" {
		result.StopReason = core.StopReasonStop
	}
	result.Usage.ContextTokens = result.Usage.InputTokens + result.Usage.CacheReadTokens
	result.Usage.UsageSemanticsVersion = "v2"
	result.Latency = time.Since(started)
	return validateNativeStreamResult(result)
}

func (c *Client) streamGemini(ctx context.Context, request okruntime.ModelRequest, onDelta func(string) error) (core.ModelResult, error) {
	result, model, key, err := c.resultBase(request)
	if err != nil {
		return result, err
	}
	endpoint := c.endpoint + "/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse&key=" + url.QueryEscape(key)
	payload := geminiPayload(request)
	result.RequestBytes = serializedPayloadBytes(payload)
	response, started, err := c.postAccept(ctx, endpoint, payload, nil, "text/event-stream")
	if err != nil {
		result.StopReason, result.ErrorCode = stopForError(err), errorCode(err)
		return result, err
	}
	result.FirstByteLatency = time.Since(started)
	defer response.Body.Close()
	result.ProviderRequestID = response.Header.Get("x-request-id")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		discardProviderBody(response.Body)
		result.StopReason, result.ErrorCode = core.StopReasonError, providerErrorCode(response.StatusCode)
		return result, &GatewayError{Code: result.ErrorCode}
	}
	var visible strings.Builder
	scanErr := modeltransport.ScanLinesWithProgress(ctx, response.Body, io.LimitReader(response.Body, 16<<20), 4<<20, modeltransport.Remaining(c.firstToken, started), c.streamIdle, func(rawLine string) (bool, error) {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "data:") {
			return false, nil
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			return false, nil
		}
		var chunk struct {
			Candidates []struct {
				FinishReason string `json:"finishReason"`
				Content      struct {
					Parts []struct {
						Text         string `json:"text"`
						Thought      bool   `json:"thought"`
						FunctionCall *struct {
							Name string         `json:"name"`
							Args map[string]any `json:"args"`
						} `json:"functionCall"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
			Usage struct {
				PromptTokens, CandidateTokens, CachedTokens, ThoughtsTokens int64
			} `json:"-"`
			UsageRaw struct {
				PromptTokens    int64 `json:"promptTokenCount"`
				CandidateTokens int64 `json:"candidatesTokenCount"`
				CachedTokens    int64 `json:"cachedContentTokenCount"`
				ThoughtsTokens  int64 `json:"thoughtsTokenCount"`
			} `json:"usageMetadata"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil || len(chunk.Candidates) == 0 {
			result.StopReason, result.ErrorCode = core.StopReasonError, "PROVIDER_PROTOCOL_ERROR"
			return false, &GatewayError{Code: result.ErrorCode}
		}
		progressed := false
		candidate := chunk.Candidates[0]
		if candidate.FinishReason != "" {
			result.StopReason = nativeStopReason(candidate.FinishReason, false)
		}
		for _, part := range candidate.Content.Parts {
			if part.FunctionCall != nil {
				progressed = true
				call := core.ToolCall{ID: uuid.NewString(), Name: part.FunctionCall.Name, Arguments: part.FunctionCall.Args}
				result.ToolCalls = append(result.ToolCalls, call)
				result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockToolCall, ToolCall: &core.ToolCallBlock{ID: call.ID, Name: call.Name, Arguments: call.Arguments}})
			} else if part.Thought {
				progressed = progressed || part.Text != ""
				result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockThinking, Thinking: &core.ThinkingBlock{Text: part.Text}})
			} else if part.Text != "" {
				progressed = true
				visible.WriteString(part.Text)
				if result.FirstSafeLatency == 0 {
					result.FirstSafeLatency = time.Since(started)
				}
				result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockText, Text: part.Text})
				if onDelta != nil {
					if deltaErr := onDelta(part.Text); deltaErr != nil {
						result.StopReason = core.StopReasonAborted
						return progressed, deltaErr
					}
				}
			}
		}
		if chunk.UsageRaw.PromptTokens > 0 || chunk.UsageRaw.CandidateTokens > 0 {
			inputTokens := chunk.UsageRaw.PromptTokens - chunk.UsageRaw.CachedTokens
			if inputTokens < 0 {
				inputTokens = 0
			}
			result.Usage = core.ModelUsage{
				InputTokens: inputTokens, OutputTokens: chunk.UsageRaw.CandidateTokens,
				CachedTokens: chunk.UsageRaw.CachedTokens, CacheReadTokens: chunk.UsageRaw.CachedTokens,
				ContextTokens: chunk.UsageRaw.PromptTokens, ReasoningTokens: chunk.UsageRaw.ThoughtsTokens,
				UsageSemanticsVersion: "v2",
			}
		}
		return progressed, nil
	})
	if scanErr != nil {
		if ctx.Err() != nil {
			result.StopReason, result.ErrorCode = core.StopReasonAborted, "MODEL_ABORTED"
			return result, ctx.Err()
		}
		var gatewayErr *GatewayError
		if errors.As(scanErr, &gatewayErr) {
			return result, scanErr
		}
		result.StopReason, result.ErrorCode = core.StopReasonError, "PROVIDER_UNAVAILABLE"
		return result, &GatewayError{Code: result.ErrorCode, Cause: scanErr}
	}
	result.Content = strings.TrimSpace(visible.String())
	if len(result.ToolCalls) > 0 {
		result.StopReason = core.StopReasonToolUse
	} else if result.StopReason == "" {
		result.StopReason = core.StopReasonStop
	}
	result.Latency = time.Since(started)
	return validateNativeStreamResult(result)
}

func validateNativeStreamResult(result core.ModelResult) (core.ModelResult, error) {
	if result.Content == "" && len(result.ToolCalls) == 0 && len(result.ContentBlocks) == 0 {
		result.StopReason, result.ErrorCode = core.StopReasonError, "PROVIDER_EMPTY_RESPONSE"
		return result, &GatewayError{Code: result.ErrorCode}
	}
	return result, nil
}

func (c *Client) DefaultModel(_ context.Context) (string, error) {
	if c.model == "" {
		return "", &GatewayError{Code: "MODEL_DISCOVERY_EMPTY"}
	}
	return c.model, nil
}

func (c *Client) ListModels(ctx context.Context) ([]core.ModelDescriptor, error) {
	key := c.credential()
	if key == "" {
		return nil, &GatewayError{Code: "MODEL_CREDENTIAL_MISSING"}
	}
	var endpoint string
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://invalid", nil)
	if err != nil {
		return nil, err
	}
	switch c.protocol {
	case ProtocolAnthropic:
		endpoint = c.endpoint + "/models"
		request, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err == nil {
			request.Header.Set("x-api-key", key)
			request.Header.Set("anthropic-version", "2023-06-01")
		}
	case ProtocolGemini:
		endpoint = c.endpoint + "/models?key=" + url.QueryEscape(key)
		request, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	default:
		return nil, &GatewayError{Code: "PROVIDER_PROTOCOL_UNSUPPORTED"}
	}
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := modeltransport.Do(c.httpClient, request)
	if err != nil {
		return nil, &GatewayError{Code: "MODEL_DISCOVERY_FAILED", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		discardProviderBody(response.Body)
		return nil, &GatewayError{Code: providerErrorCode(response.StatusCode)}
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name             string   `json:"name"`
			SupportedMethods []string `json:"supportedGenerationMethods"`
			InputTokenLimit  int64    `json:"inputTokenLimit"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, &GatewayError{Code: "MODEL_DISCOVERY_FAILED"}
	}
	models := make([]core.ModelDescriptor, 0, len(payload.Data)+len(payload.Models))
	for _, item := range payload.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			models = append(models, core.ModelDescriptor{ID: id, ToolCalling: true, ToolCallingKnown: true})
		}
	}
	for _, item := range payload.Models {
		if !contains(item.SupportedMethods, "generateContent") {
			continue
		}
		id := strings.TrimPrefix(strings.TrimSpace(item.Name), "models/")
		if id != "" {
			models = append(models, core.ModelDescriptor{ID: id, ToolCalling: true, ToolCallingKnown: true, Vision: true, Reasoning: true, ContextWindow: item.InputTokenLimit})
		}
	}
	if len(models) == 0 && c.model != "" {
		models = append(models, core.ModelDescriptor{ID: c.model})
	}
	return models, nil
}

func (c *Client) completeAnthropic(ctx context.Context, request okruntime.ModelRequest) (core.ModelResult, error) {
	result, model, key, err := c.resultBase(request)
	if err != nil {
		return result, err
	}
	payload := anthropicPayload(request, model)
	result.RequestBytes = serializedPayloadBytes(payload)
	response, started, err := c.post(ctx, c.endpoint+"/messages", payload, map[string]string{
		"x-api-key": key, "anthropic-version": "2023-06-01",
	})
	if err != nil {
		result.StopReason, result.ErrorCode = stopForError(err), errorCode(err)
		return result, err
	}
	defer response.Body.Close()
	result.FirstByteLatency = time.Since(started)
	result.Latency = result.FirstByteLatency
	result.ProviderRequestID = firstNonEmpty(response.Header.Get("request-id"), response.Header.Get("x-request-id"))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		discardProviderBody(response.Body)
		result.StopReason, result.ErrorCode = core.StopReasonError, providerErrorCode(response.StatusCode)
		return result, &GatewayError{Code: result.ErrorCode}
	}
	var decoded struct {
		ID         string `json:"id"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&decoded); err != nil {
		result.StopReason, result.ErrorCode = core.StopReasonError, "PROVIDER_PROTOCOL_ERROR"
		return result, &GatewayError{Code: result.ErrorCode}
	}
	if decoded.ID != "" {
		result.ProviderRequestID = decoded.ID
	}
	var text strings.Builder
	for _, block := range decoded.Content {
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
			result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockText, Text: block.Text})
		case "thinking":
			result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockThinking, Thinking: &core.ThinkingBlock{Text: block.Text}})
		case "tool_use":
			callID := block.ID
			if callID == "" {
				callID = uuid.NewString()
			}
			call := core.ToolCall{ID: callID, Name: block.Name, Arguments: block.Input}
			result.ToolCalls = append(result.ToolCalls, call)
			result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockToolCall, ToolCall: &core.ToolCallBlock{ID: call.ID, Name: call.Name, Arguments: call.Arguments}})
		}
	}
	result.Content = strings.TrimSpace(text.String())
	if result.Content != "" {
		result.FirstSafeLatency = time.Since(started)
	}
	result.Latency = time.Since(started)
	result.StopReason = nativeStopReason(decoded.StopReason, len(result.ToolCalls) > 0)
	result.Usage = core.ModelUsage{
		InputTokens: decoded.Usage.InputTokens, OutputTokens: decoded.Usage.OutputTokens,
		CacheReadTokens: decoded.Usage.CacheReadInputTokens, CachedTokens: decoded.Usage.CacheReadInputTokens,
		CacheWriteTokens:      decoded.Usage.CacheCreationInputTokens,
		ContextTokens:         decoded.Usage.InputTokens + decoded.Usage.CacheReadInputTokens,
		UsageSemanticsVersion: "v2",
	}
	if result.Content == "" && len(result.ToolCalls) == 0 && len(result.ContentBlocks) == 0 {
		result.StopReason, result.ErrorCode = core.StopReasonError, "PROVIDER_EMPTY_RESPONSE"
		return result, &GatewayError{Code: result.ErrorCode}
	}
	return result, nil
}

func (c *Client) completeGemini(ctx context.Context, request okruntime.ModelRequest) (core.ModelResult, error) {
	result, model, key, err := c.resultBase(request)
	if err != nil {
		return result, err
	}
	payload := geminiPayload(request)
	result.RequestBytes = serializedPayloadBytes(payload)
	endpoint := c.endpoint + "/models/" + url.PathEscape(model) + ":generateContent?key=" + url.QueryEscape(key)
	response, started, err := c.post(ctx, endpoint, payload, nil)
	if err != nil {
		result.StopReason, result.ErrorCode = stopForError(err), errorCode(err)
		return result, err
	}
	defer response.Body.Close()
	result.FirstByteLatency = time.Since(started)
	result.Latency = result.FirstByteLatency
	result.ProviderRequestID = response.Header.Get("x-request-id")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		discardProviderBody(response.Body)
		result.StopReason, result.ErrorCode = core.StopReasonError, providerErrorCode(response.StatusCode)
		return result, &GatewayError{Code: result.ErrorCode}
	}
	var decoded struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text         string `json:"text"`
					Thought      bool   `json:"thought"`
					FunctionCall *struct {
						Name string         `json:"name"`
						Args map[string]any `json:"args"`
					} `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Usage struct {
			PromptTokens    int64 `json:"promptTokenCount"`
			CandidateTokens int64 `json:"candidatesTokenCount"`
			CachedTokens    int64 `json:"cachedContentTokenCount"`
			ThoughtsTokens  int64 `json:"thoughtsTokenCount"`
			TotalTokenCount int64 `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&decoded); err != nil || len(decoded.Candidates) == 0 {
		result.StopReason, result.ErrorCode = core.StopReasonError, "PROVIDER_PROTOCOL_ERROR"
		return result, &GatewayError{Code: result.ErrorCode}
	}
	var text strings.Builder
	for _, part := range decoded.Candidates[0].Content.Parts {
		if part.FunctionCall != nil {
			call := core.ToolCall{ID: uuid.NewString(), Name: part.FunctionCall.Name, Arguments: part.FunctionCall.Args}
			result.ToolCalls = append(result.ToolCalls, call)
			result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockToolCall, ToolCall: &core.ToolCallBlock{ID: call.ID, Name: call.Name, Arguments: call.Arguments}})
		} else if part.Thought {
			result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockThinking, Thinking: &core.ThinkingBlock{Text: part.Text}})
		} else if part.Text != "" {
			text.WriteString(part.Text)
			result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockText, Text: part.Text})
		}
	}
	result.Content = strings.TrimSpace(text.String())
	if result.Content != "" {
		result.FirstSafeLatency = time.Since(started)
	}
	result.Latency = time.Since(started)
	result.StopReason = nativeStopReason(decoded.Candidates[0].FinishReason, len(result.ToolCalls) > 0)
	inputTokens := decoded.Usage.PromptTokens - decoded.Usage.CachedTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	result.Usage = core.ModelUsage{
		InputTokens: inputTokens, OutputTokens: decoded.Usage.CandidateTokens,
		CachedTokens: decoded.Usage.CachedTokens, CacheReadTokens: decoded.Usage.CachedTokens,
		ContextTokens: decoded.Usage.PromptTokens, ReasoningTokens: decoded.Usage.ThoughtsTokens,
		UsageSemanticsVersion: "v2",
	}
	if result.Content == "" && len(result.ToolCalls) == 0 && len(result.ContentBlocks) == 0 {
		result.StopReason, result.ErrorCode = core.StopReasonError, "PROVIDER_EMPTY_RESPONSE"
		return result, &GatewayError{Code: result.ErrorCode}
	}
	return result, nil
}

func (c *Client) resultBase(request okruntime.ModelRequest) (core.ModelResult, string, string, error) {
	callID := uuid.NewString()
	model := strings.TrimSpace(request.Model)
	if model == "" {
		model = strings.TrimSpace(request.RequestedModel.ModelID)
	}
	if model == "" {
		model = c.model
	}
	result := core.ModelResult{
		CallID: callID, Provider: c.protocol, Endpoint: c.endpoint, Model: model,
		RequestedModel: request.RequestedModel,
		EffectiveModel: core.ModelRef{ProviderID: firstNonEmpty(request.RequestedModel.ProviderID, c.protocol), ModelID: model},
		RoutingReason:  "provider_instance",
	}
	if request.EnableTools && request.ToolCallingKnown && !request.ToolCallingAllowed {
		result.StopReason, result.ErrorCode = core.StopReasonError, "MODEL_CAPABILITY_MISMATCH"
		return result, model, "", &GatewayError{Code: result.ErrorCode}
	}
	key := c.credential()
	if key == "" {
		result.StopReason, result.ErrorCode = core.StopReasonError, "MODEL_CREDENTIAL_MISSING"
		return result, model, "", &GatewayError{Code: result.ErrorCode}
	}
	if model == "" {
		result.StopReason, result.ErrorCode = core.StopReasonError, "MODEL_DISCOVERY_EMPTY"
		return result, "", key, &GatewayError{Code: result.ErrorCode}
	}
	return result, model, key, nil
}

func (c *Client) credential() string {
	if c.key == nil {
		return ""
	}
	return strings.TrimSpace(c.key())
}

func (c *Client) post(ctx context.Context, endpoint string, payload any, headers map[string]string) (*http.Response, time.Time, error) {
	return c.postAccept(ctx, endpoint, payload, headers, "application/json")
}

func (c *Client) postAccept(ctx context.Context, endpoint string, payload any, headers map[string]string, accept string) (*http.Response, time.Time, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, time.Time{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, time.Time{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", "Oktapod/0.1")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	started := time.Now()
	response, err := modeltransport.Do(c.httpClient, request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, started, ctx.Err()
		}
		return nil, started, &GatewayError{Code: "PROVIDER_UNAVAILABLE", Cause: err}
	}
	return response, started, nil
}

func serializedPayloadBytes(payload any) int64 {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0
	}
	return int64(len(body))
}

func anthropicPayload(request okruntime.ModelRequest, model string) map[string]any {
	payload := map[string]any{
		"model": model, "max_tokens": maxOutput(request.MaxOutputTokens),
		"system": request.SystemPrompt, "messages": anthropicMessages(request),
	}
	if request.EnableTools {
		tools := make([]map[string]any, 0, len(request.ToolDefinitions))
		for _, tool := range filteredTools(request) {
			tools = append(tools, map[string]any{"name": tool.Name, "description": tool.Description, "input_schema": tool.Parameters})
		}
		if len(tools) > 0 {
			payload["tools"] = tools
		}
	}
	return payload
}

func geminiPayload(request okruntime.ModelRequest) map[string]any {
	payload := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]any{{"text": request.SystemPrompt}}},
		"contents":          geminiContents(request),
		"generationConfig":  map[string]any{"maxOutputTokens": maxOutput(request.MaxOutputTokens)},
	}
	if request.EnableTools {
		declarations := make([]map[string]any, 0, len(request.ToolDefinitions))
		for _, tool := range filteredTools(request) {
			declarations = append(declarations, map[string]any{"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters})
		}
		if len(declarations) > 0 {
			payload["tools"] = []map[string]any{{"functionDeclarations": declarations}}
		}
	}
	return payload
}

func anthropicMessages(request okruntime.ModelRequest) []map[string]any {
	if len(request.Messages) == 0 {
		return []map[string]any{{"role": "user", "content": []map[string]any{{"type": "text", "text": request.UserPrompt}}}}
	}
	messages := make([]map[string]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		role := message.Role
		if role != core.MessageRoleAssistant {
			role = core.MessageRoleUser
		}
		blocks := make([]map[string]any, 0, len(message.Content))
		for _, block := range message.Content {
			switch block.Type {
			case core.ContentBlockToolCall:
				if block.ToolCall != nil {
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": block.ToolCall.ID, "name": block.ToolCall.Name, "input": block.ToolCall.Arguments})
				}
			case core.ContentBlockToolResult:
				if block.ToolResult != nil {
					blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": block.ToolResult.ToolCallID, "content": block.ToolResult.Content, "is_error": block.ToolResult.IsError})
				}
			default:
				if block.Text != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": block.Text})
				}
			}
		}
		messages = append(messages, map[string]any{"role": string(role), "content": blocks})
	}
	return messages
}

func geminiContents(request okruntime.ModelRequest) []map[string]any {
	if len(request.Messages) == 0 {
		return []map[string]any{{"role": "user", "parts": []map[string]any{{"text": request.UserPrompt}}}}
	}
	contents := make([]map[string]any, 0, len(request.Messages))
	for _, message := range request.Messages {
		role := "user"
		if message.Role == core.MessageRoleAssistant {
			role = "model"
		}
		parts := make([]map[string]any, 0, len(message.Content))
		for _, block := range message.Content {
			switch block.Type {
			case core.ContentBlockToolCall:
				if block.ToolCall != nil {
					parts = append(parts, map[string]any{"functionCall": map[string]any{"name": block.ToolCall.Name, "args": block.ToolCall.Arguments}})
				}
			case core.ContentBlockToolResult:
				if block.ToolResult != nil {
					parts = append(parts, map[string]any{"functionResponse": map[string]any{"name": block.ToolResult.ToolName, "response": map[string]any{"content": block.ToolResult.Content, "is_error": block.ToolResult.IsError}}})
				}
			default:
				if block.Text != "" {
					parts = append(parts, map[string]any{"text": block.Text})
				}
			}
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	return contents
}

func filteredTools(request okruntime.ModelRequest) []okruntime.ToolDefinition {
	if !request.ToolScopeDeclared {
		return request.ToolDefinitions
	}
	allowed := make(map[string]struct{}, len(request.AllowedTools))
	for _, name := range request.AllowedTools {
		allowed[name] = struct{}{}
	}
	result := make([]okruntime.ToolDefinition, 0, len(request.ToolDefinitions))
	for _, tool := range request.ToolDefinitions {
		if _, ok := allowed[tool.Name]; ok {
			result = append(result, tool)
		}
	}
	return result
}

func maxOutput(value int) int {
	if value <= 0 {
		return 4096
	}
	return value
}

func nativeStopReason(value string, tools bool) core.StopReason {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tool_use", "function_call":
		return core.StopReasonToolUse
	case "max_tokens", "max_tokens_reached", "max_output_tokens":
		return core.StopReasonLength
	case "safety", "recitation", "blocked", "error":
		return core.StopReasonError
	default:
		if tools {
			return core.StopReasonToolUse
		}
		return core.StopReasonStop
	}
}

func providerErrorCode(status int) string {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "PROVIDER_AUTH"
	case http.StatusTooManyRequests:
		return "PROVIDER_RATE_LIMIT"
	default:
		if status >= 500 {
			return "PROVIDER_UNAVAILABLE"
		}
		return "PROVIDER_REJECTED"
	}
}

func stopForError(err error) core.StopReason {
	if errors.Is(err, context.Canceled) {
		return core.StopReasonAborted
	}
	return core.StopReasonError
}

func errorCode(err error) string {
	var gateway *GatewayError
	if errors.As(err, &gateway) {
		return gateway.Code
	}
	if errors.Is(err, context.Canceled) {
		return "MODEL_ABORTED"
	}
	return "PROVIDER_UNAVAILABLE"
}

func discardProviderBody(reader io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(reader, 8<<10))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
