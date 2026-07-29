package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

type Client struct {
	endpoint   string
	explicit   string
	key        func() string
	httpClient *http.Client

	mu              sync.Mutex
	discoveredModel string
	catalog         []core.ModelDescriptor
	catalogAt       time.Time
}

type GatewayError struct {
	Code       string
	StatusCode int
	Message    string
}

func (e *GatewayError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s: provider returned HTTP %d: %s", e.Code, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *GatewayError) ErrorCode() string { return e.Code }

func New(endpoint, model string, key func() string) *Client {
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		explicit: strings.TrimSpace(model),
		key:      key,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
		},
	}
}

func (c *Client) Complete(ctx context.Context, request okruntime.ModelRequest) (core.ModelResult, error) {
	result, err := c.completeOnce(ctx, request)
	normalizeModelError(&result, err)
	if err == nil || strings.TrimSpace(request.FallbackModel) == "" || !isFallbackEligible(result.ErrorCode) {
		return result, err
	}
	fallbackRequest := request
	fallbackRequest.Model = strings.TrimSpace(request.FallbackModel)
	fallbackRequest.FallbackModel = ""
	fallbackResult, fallbackErr := c.completeOnce(ctx, fallbackRequest)
	normalizeModelError(&fallbackResult, fallbackErr)
	fallbackResult.RoutingReason = "fallback"
	if fallbackErr == nil {
		return fallbackResult, nil
	}
	return fallbackResult, fallbackErr
}

// Stream emits assistant content as provider deltas while returning the same
// final audit and usage record as Complete. Fallback is only attempted before
// any content has reached the caller, avoiding mixed-model responses.
func (c *Client) Stream(ctx context.Context, request okruntime.ModelRequest, onDelta func(string) error) (core.ModelResult, error) {
	emitted := false
	emit := func(delta string) error {
		if delta == "" {
			return nil
		}
		emitted = true
		if onDelta == nil {
			return nil
		}
		return onDelta(delta)
	}
	result, err := c.streamOnce(ctx, request, emit)
	normalizeModelError(&result, err)
	if err == nil || emitted || strings.TrimSpace(request.FallbackModel) == "" || !isFallbackEligible(result.ErrorCode) {
		return result, err
	}
	fallbackRequest := request
	fallbackRequest.Model = strings.TrimSpace(request.FallbackModel)
	fallbackRequest.FallbackModel = ""
	fallbackResult, fallbackErr := c.streamOnce(ctx, fallbackRequest, emit)
	normalizeModelError(&fallbackResult, fallbackErr)
	fallbackResult.RoutingReason = "fallback"
	return fallbackResult, fallbackErr
}

func (c *Client) streamOnce(ctx context.Context, request okruntime.ModelRequest, onDelta func(string) error) (core.ModelResult, error) {
	callID, err := uuid.NewV7()
	if err != nil {
		return core.ModelResult{}, err
	}
	result := core.ModelResult{CallID: callID.String(), Provider: "openai_compatible", Endpoint: c.endpoint, RoutingReason: "project_default", RequestedModel: requestedModelRef(request)}
	key := strings.TrimSpace(c.key())
	if key == "" {
		result.ErrorCode = "MODEL_CREDENTIAL_MISSING"
		return result, &GatewayError{Code: result.ErrorCode, Message: "configure model credentials with `oktapod model configure`"}
	}
	model := strings.TrimSpace(request.Model)
	routingReason := "agent_override"
	if model == "" {
		model, routingReason, err = c.resolveModel(ctx, key)
	}
	result.Model, result.RoutingReason = model, routingReason
	result.RequestedModel = requestedModelRef(request)
	result.EffectiveModel = core.ModelRef{ProviderID: "openai_compatible", ModelID: model}
	if err != nil {
		if gatewayErr, ok := err.(*GatewayError); ok {
			result.ErrorCode = gatewayErr.Code
		}
		return result, err
	}
	if request.EnableTools {
		if capabilityErr := c.validateToolCalling(ctx, key, model); capabilityErr != nil {
			result.ErrorCode = capabilityErr.Code
			return result, capabilityErr
		}
	}
	payload := chatRequest{
		Model: model, MaxTokens: request.MaxOutputTokens, Stream: true,
		Messages: chatMessagesFromRequest(request),
	}
	if request.EnableTools {
		payload.Tools = []chatTool{}
		for _, definition := range request.ToolDefinitions {
			if strings.TrimSpace(definition.Name) == "" {
				continue
			}
			payload.Tools = append(payload.Tools, chatTool{Type: "function", Function: chatToolFunction{
				Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters,
			}})
		}
		if request.ToolScopeDeclared {
			payload.Tools = filterBuiltinTools(payload.Tools, request.AllowedTools)
		}
		if len(payload.Tools) > 0 {
			payload.ToolChoice = "auto"
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+key)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream, application/json")
	httpRequest.Header.Set("User-Agent", "Oktapod/0.1")
	started := time.Now()
	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			result.ErrorCode = "MODEL_ABORTED"
			return result, ctx.Err()
		}
		result.ErrorCode = "PROVIDER_UNAVAILABLE"
		return result, &GatewayError{Code: result.ErrorCode, Message: err.Error()}
	}
	defer response.Body.Close()
	result.ProviderRequestID = response.Header.Get("x-request-id")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.ErrorCode = providerErrorCode(response.StatusCode)
		return result, &GatewayError{Code: result.ErrorCode, StatusCode: response.StatusCode, Message: safeProviderBody(response.Body)}
	}

	var content strings.Builder
	var thinking strings.Builder
	type streamedTool struct {
		id        string
		name      string
		arguments strings.Builder
	}
	streamedTools := map[int]*streamedTool{}
	finishReason := ""
	if !strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		var decoded chatResponse
		if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&decoded); err != nil || len(decoded.Choices) == 0 {
			result.ErrorCode = "PROVIDER_PROTOCOL_ERROR"
			return result, &GatewayError{Code: result.ErrorCode, Message: "invalid chat completion response"}
		}
		if err := applyResponseMessage(&result, decoded.Choices[0].Message, decoded.Choices[0].FinishReason); err != nil {
			return result, err
		}
		if err := onDelta(result.Content); err != nil {
			return result, err
		}
		result.Usage = usageFromChat(decoded.Usage)
		if decoded.Id != "" {
			result.ProviderRequestID = decoded.Id
		}
	} else {
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 64<<10), 2<<20)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			var chunk chatStreamResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				result.ErrorCode = "PROVIDER_PROTOCOL_ERROR"
				return result, &GatewayError{Code: result.ErrorCode, Message: "invalid streaming chat completion chunk"}
			}
			if chunk.Id != "" {
				result.ProviderRequestID = chunk.Id
			}
			chunkUsage := usageFromChat(chunk.Usage)
			if chunkUsage.InputTokens > 0 || chunkUsage.OutputTokens > 0 || chunkUsage.CachedTokens > 0 || chunkUsage.ReasoningTokens > 0 {
				result.Usage = chunkUsage
			}
			for _, choice := range chunk.Choices {
				if choice.FinishReason != "" {
					finishReason = choice.FinishReason
				}
				if choice.Delta.ReasoningContent != "" {
					thinking.WriteString(choice.Delta.ReasoningContent)
				}
				for _, delta := range choice.Delta.ToolCalls {
					current := streamedTools[delta.Index]
					if current == nil {
						current = &streamedTool{}
						streamedTools[delta.Index] = current
					}
					if delta.ID != "" {
						current.id = delta.ID
					}
					if delta.Function.Name != "" {
						current.name += delta.Function.Name
					}
					current.arguments.WriteString(delta.Function.Arguments)
				}
				if choice.Delta.Content != "" {
					content.WriteString(choice.Delta.Content)
					if err := onDelta(choice.Delta.Content); err != nil {
						return result, err
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			result.ErrorCode = "PROVIDER_UNAVAILABLE"
			return result, &GatewayError{Code: result.ErrorCode, Message: err.Error()}
		}
	}
	result.Latency = time.Since(started)
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		streamMessage := responseChatMessage{Role: "assistant", ReasoningContent: thinking.String()}
		value := content.String()
		if value != "" {
			streamMessage.Content = &value
		}
		indexes := make([]int, 0, len(streamedTools))
		for index := range streamedTools {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		for _, index := range indexes {
			item := streamedTools[index]
			var call chatResponseToolCall
			call.Id = item.id
			call.Type = "function"
			call.Function.Name = item.name
			call.Function.Arguments = chatFunctionArguments(item.arguments.String())
			streamMessage.ToolCalls = append(streamMessage.ToolCalls, call)
		}
		if err := applyResponseMessage(&result, streamMessage, finishReason); err != nil {
			return result, err
		}
	}
	if result.Usage.InputTokens == 0 {
		result.Usage = core.ModelUsage{InputTokens: estimateTokens(modelRequestText(request)), ContextTokens: estimateTokens(modelRequestText(request)), OutputTokens: estimateTokens(result.Content), UsageSemanticsVersion: "v2", Estimated: true}
	}
	return result, nil
}

func (c *Client) completeOnce(ctx context.Context, request okruntime.ModelRequest) (core.ModelResult, error) {
	callID, err := uuid.NewV7()
	if err != nil {
		return core.ModelResult{}, err
	}
	result := core.ModelResult{
		CallID:         callID.String(),
		Provider:       "openai_compatible",
		Endpoint:       c.endpoint,
		RoutingReason:  "project_default",
		RequestedModel: requestedModelRef(request),
	}
	key := strings.TrimSpace(c.key())
	if key == "" {
		result.ErrorCode = "MODEL_CREDENTIAL_MISSING"
		return result, &GatewayError{Code: result.ErrorCode, Message: "configure model credentials with `oktapod model configure`"}
	}

	model := strings.TrimSpace(request.Model)
	routingReason := "agent_override"
	if model == "" {
		model, routingReason, err = c.resolveModel(ctx, key)
	}
	result.Model = model
	result.RoutingReason = routingReason
	result.RequestedModel = requestedModelRef(request)
	result.EffectiveModel = core.ModelRef{ProviderID: "openai_compatible", ModelID: model}
	if err != nil {
		if gatewayErr, ok := err.(*GatewayError); ok {
			result.ErrorCode = gatewayErr.Code
		}
		return result, err
	}
	if request.EnableTools {
		if capabilityErr := c.validateToolCalling(ctx, key, model); capabilityErr != nil {
			result.ErrorCode = capabilityErr.Code
			return result, capabilityErr
		}
	}

	payload := chatRequest{
		Model:     model,
		MaxTokens: request.MaxOutputTokens,
		Messages:  chatMessagesFromRequest(request),
		Stream:    false,
	}
	if request.EnableTools {
		payload.Tools = []chatTool{}
		for _, definition := range request.ToolDefinitions {
			if strings.TrimSpace(definition.Name) == "" {
				continue
			}
			payload.Tools = append(payload.Tools, chatTool{Type: "function", Function: chatToolFunction{
				Name: definition.Name, Description: definition.Description, Parameters: definition.Parameters,
			}})
		}
		if request.ToolScopeDeclared {
			payload.Tools = filterBuiltinTools(payload.Tools, request.AllowedTools)
		}
		if len(payload.Tools) > 0 {
			payload.ToolChoice = "auto"
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return result, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+key)
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", "Oktapod/0.1")

	started := time.Now()
	response, err := c.httpClient.Do(httpRequest)
	result.Latency = time.Since(started)
	if err != nil {
		if ctx.Err() != nil {
			result.ErrorCode = "MODEL_ABORTED"
			return result, ctx.Err()
		}
		result.ErrorCode = "PROVIDER_UNAVAILABLE"
		return result, &GatewayError{Code: result.ErrorCode, Message: err.Error()}
	}
	defer response.Body.Close()
	result.ProviderRequestID = response.Header.Get("x-request-id")
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.ErrorCode = providerErrorCode(response.StatusCode)
		return result, &GatewayError{Code: result.ErrorCode, StatusCode: response.StatusCode, Message: safeProviderBody(response.Body)}
	}
	var decoded chatResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&decoded); err != nil {
		result.ErrorCode = "PROVIDER_PROTOCOL_ERROR"
		return result, &GatewayError{Code: result.ErrorCode, Message: "invalid chat completion response"}
	}
	if len(decoded.Choices) == 0 {
		result.ErrorCode = "PROVIDER_EMPTY_RESPONSE"
		return result, &GatewayError{Code: result.ErrorCode, Message: "provider returned no assistant content"}
	}
	message := decoded.Choices[0].Message
	result.Usage = usageFromChat(decoded.Usage)
	if result.Usage.Estimated {
		result.Usage.InputTokens = estimateTokens(modelRequestText(request))
		result.Usage.ContextTokens = result.Usage.InputTokens
	}
	if decoded.Id != "" {
		result.ProviderRequestID = decoded.Id
	}
	if err := applyResponseMessage(&result, message, decoded.Choices[0].FinishReason); err != nil {
		return result, err
	}
	if result.Usage.Estimated {
		result.Usage.OutputTokens = estimateTokens(result.Content)
	}
	return result, nil
}

func isFallbackEligible(code string) bool {
	switch code {
	case "PROVIDER_UNAVAILABLE", "PROVIDER_RATE_LIMIT", "PROVIDER_REJECTED", "PROVIDER_MODEL_NOT_FOUND", "PROVIDER_PROTOCOL_ERROR", "PROVIDER_EMPTY_RESPONSE", "MODEL_CAPABILITY_MISMATCH":
		return true
	default:
		return false
	}
}

func (c *Client) resolveModel(ctx context.Context, key string) (string, string, error) {
	if c.explicit != "" {
		return c.explicit, "explicit_configuration", nil
	}
	c.mu.Lock()
	cached := c.discoveredModel
	c.mu.Unlock()
	if cached != "" {
		return cached, "cached_auto_discovery", nil
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/models", nil)
	if err != nil {
		return "", "auto_discovery", err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Oktapod/0.1")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", "auto_discovery", &GatewayError{Code: "MODEL_DISCOVERY_FAILED", Message: err.Error()}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "auto_discovery", &GatewayError{Code: providerErrorCode(response.StatusCode), StatusCode: response.StatusCode, Message: safeProviderBody(response.Body)}
	}
	var catalog modelList
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&catalog); err != nil {
		return "", "auto_discovery", &GatewayError{Code: "MODEL_DISCOVERY_FAILED", Message: "invalid model catalog"}
	}
	model := chooseModel(catalog.Data)
	if model == "" {
		return "", "auto_discovery", &GatewayError{Code: "MODEL_DISCOVERY_EMPTY", Message: "provider returned no chat-capable model"}
	}
	c.mu.Lock()
	c.discoveredModel = model
	c.mu.Unlock()
	return model, "auto_discovered", nil
}

// ListModels exposes the same bounded provider catalog used by automatic
// discovery. It returns stable ids plus non-sensitive capability metadata and
// redacts provider response bodies on all failures so credentials or gateway
// diagnostics cannot escape through the control-plane API.
func (c *Client) ListModels(ctx context.Context) ([]core.ModelDescriptor, error) {
	key := ""
	if c.key != nil {
		key = strings.TrimSpace(c.key())
	}
	if key == "" {
		if c.explicit != "" {
			return []core.ModelDescriptor{{ID: c.explicit}}, nil
		}
		return nil, &GatewayError{Code: "MODEL_CREDENTIAL_MISSING", Message: "configure the model credential before listing models"}
	}
	models, err := c.listModels(ctx, key)
	if err != nil && c.explicit != "" {
		return []core.ModelDescriptor{{ID: c.explicit}}, nil
	}
	return models, err
}

func (c *Client) DefaultModel(ctx context.Context) (string, error) {
	if c.explicit != "" {
		return c.explicit, nil
	}
	key := strings.TrimSpace(c.key())
	if key == "" {
		return "", &GatewayError{Code: "MODEL_CREDENTIAL_MISSING", Message: "configure the model credential before resolving the default model"}
	}
	model, _, err := c.resolveModel(ctx, key)
	return model, err
}

func (c *Client) listModels(ctx context.Context, key string) ([]core.ModelDescriptor, error) {
	c.mu.Lock()
	if len(c.catalog) > 0 && time.Since(c.catalogAt) < 30*time.Second {
		models := append([]core.ModelDescriptor(nil), c.catalog...)
		c.mu.Unlock()
		return models, nil
	}
	c.mu.Unlock()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/models", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "Oktapod/0.1")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, &GatewayError{Code: "MODEL_DISCOVERY_FAILED", Message: err.Error()}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &GatewayError{Code: providerErrorCode(response.StatusCode), StatusCode: response.StatusCode, Message: safeProviderBody(response.Body)}
	}
	var catalog modelList
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&catalog); err != nil {
		return nil, &GatewayError{Code: "MODEL_DISCOVERY_FAILED", Message: "invalid model catalog"}
	}
	ids := modelIDs(catalog.Data)
	if len(ids) == 0 {
		return nil, &GatewayError{Code: "MODEL_DISCOVERY_EMPTY", Message: "provider returned no chat-capable model"}
	}
	models := make([]core.ModelDescriptor, 0, len(ids))
	for _, id := range ids {
		for _, item := range catalog.Data {
			if strings.TrimSpace(item.Id) == id {
				models = append(models, modelDescriptor(item))
				break
			}
		}
	}
	c.mu.Lock()
	c.catalog = append([]core.ModelDescriptor(nil), models...)
	c.catalogAt = time.Now()
	c.mu.Unlock()
	return models, nil
}

func (c *Client) validateToolCalling(ctx context.Context, key, model string) *GatewayError {
	models, err := c.listModels(ctx, key)
	if err != nil {
		// A catalog outage must not turn a normal provider call into a new
		// availability failure. The provider remains the source of truth when
		// capability metadata is unavailable.
		return nil
	}
	for _, descriptor := range models {
		if descriptor.ID == model && descriptor.ToolCallingKnown && !descriptor.ToolCalling {
			return &GatewayError{Code: "MODEL_CAPABILITY_MISMATCH", Message: "selected model does not advertise tool calling"}
		}
	}
	return nil
}

type chatMessage struct {
	Role       string                `json:"role"`
	Content    any                   `json:"content,omitempty"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	Name       string                `json:"name,omitempty"`
	ToolCalls  []chatMessageToolCall `json:"tool_calls,omitempty"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

type chatMessageToolCall struct {
	ID       string                  `json:"id"`
	Type     string                  `json:"type"`
	Function chatMessageToolFunction `json:"function"`
}

type chatMessageToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatRequest struct {
	Model      string        `json:"model"`
	Messages   []chatMessage `json:"messages"`
	MaxTokens  int           `json:"max_tokens,omitempty"`
	Stream     bool          `json:"stream"`
	Tools      []chatTool    `json:"tools,omitempty"`
	ToolChoice string        `json:"tool_choice,omitempty"`
}

type chatUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`
	CompletionTokens    int64 `json:"completion_tokens"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CompletionTokensDetails struct {
		ReasoningTokens int64 `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type chatResponse struct {
	Id      string `json:"id"`
	Choices []struct {
		Message      responseChatMessage `json:"message"`
		FinishReason string              `json:"finish_reason"`
	} `json:"choices"`
	Usage chatUsage `json:"usage"`
}

type chatStreamResponse struct {
	Id      string `json:"id"`
	Choices []struct {
		Delta struct {
			Content          string                `json:"content"`
			ReasoningContent string                `json:"reasoning_content"`
			ToolCalls        []streamDeltaToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage chatUsage `json:"usage"`
}

type responseChatMessage struct {
	Role             string                 `json:"role"`
	Content          *string                `json:"content"`
	ReasoningContent string                 `json:"reasoning_content"`
	ToolCalls        []chatResponseToolCall `json:"tool_calls"`
}

type chatResponseToolCall struct {
	Id       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string                `json:"name"`
		Arguments chatFunctionArguments `json:"arguments"`
	} `json:"function"`
}

type streamDeltaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func chatMessagesFromRequest(request okruntime.ModelRequest) []chatMessage {
	messages := make([]chatMessage, 0, len(request.Messages)+1)
	if strings.TrimSpace(request.SystemPrompt) != "" {
		messages = append(messages, chatMessage{Role: "system", Content: request.SystemPrompt})
	}
	if len(request.Messages) == 0 {
		messages = append(messages, chatMessage{Role: "user", Content: request.UserPrompt})
		return messages
	}
	for _, message := range request.Messages {
		mapped := chatMessage{Role: string(message.Role), ToolCallID: message.ToolCallID, Name: message.ToolName}
		parts := make([]chatContentPart, 0, len(message.Content))
		var text strings.Builder
		for _, block := range message.Content {
			switch block.Type {
			case core.ContentBlockText:
				text.WriteString(block.Text)
				parts = append(parts, chatContentPart{Type: "text", Text: block.Text})
			case core.ContentBlockThinking:
				if block.Thinking != nil {
					text.WriteString(block.Thinking.Text)
					parts = append(parts, chatContentPart{Type: "text", Text: block.Thinking.Text})
				}
			case core.ContentBlockImage:
				if block.Image == nil {
					continue
				}
				url := strings.TrimSpace(block.Image.URL)
				if url == "" && block.Image.Data != "" {
					mediaType := strings.TrimSpace(block.Image.MediaType)
					if mediaType == "" {
						mediaType = "application/octet-stream"
					}
					url = "data:" + mediaType + ";base64," + block.Image.Data
				}
				if url != "" {
					parts = append(parts, chatContentPart{Type: "image_url", ImageURL: &chatImageURL{URL: url}})
				}
			case core.ContentBlockToolCall:
				if block.ToolCall == nil {
					continue
				}
				body, err := json.Marshal(block.ToolCall.Arguments)
				if err != nil {
					continue
				}
				mapped.ToolCalls = append(mapped.ToolCalls, chatMessageToolCall{ID: block.ToolCall.ID, Type: "function", Function: chatMessageToolFunction{Name: block.ToolCall.Name, Arguments: string(body)}})
			case core.ContentBlockToolResult:
				if block.ToolResult == nil {
					continue
				}
				mapped.Role = "tool"
				mapped.ToolCallID = block.ToolResult.ToolCallID
				mapped.Name = block.ToolResult.ToolName
				text.WriteString(block.ToolResult.Content)
			}
		}
		switch {
		case mapped.Role == "tool":
			mapped.Content = text.String()
		case len(parts) == 1 && parts[0].Type == "text":
			mapped.Content = parts[0].Text
		case len(parts) > 0:
			mapped.Content = parts
		case len(mapped.ToolCalls) == 0:
			mapped.Content = ""
		}
		messages = append(messages, mapped)
	}
	return messages
}

func modelRequestText(request okruntime.ModelRequest) string {
	var value strings.Builder
	value.WriteString(request.SystemPrompt)
	value.WriteByte('\n')
	if len(request.Messages) == 0 {
		value.WriteString(request.UserPrompt)
		return value.String()
	}
	for _, message := range request.Messages {
		for _, block := range message.Content {
			value.WriteString(block.Text)
			if block.Thinking != nil {
				value.WriteString(block.Thinking.Text)
			}
			if block.ToolResult != nil {
				value.WriteString(block.ToolResult.Content)
			}
		}
	}
	return value.String()
}

func requestedModelRef(request okruntime.ModelRequest) core.ModelRef {
	if request.RequestedModel.ProviderID != "" || request.RequestedModel.ModelID != "" {
		return request.RequestedModel
	}
	return core.ModelRef{ProviderID: "openai_compatible", ModelID: strings.TrimSpace(request.Model)}
}

func mapStopReason(reason string, hasTools bool) core.StopReason {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "tool_calls", "function_call":
		return core.StopReasonToolUse
	case "length", "max_tokens":
		return core.StopReasonLength
	case "content_filter", "error":
		return core.StopReasonError
	case "stop", "end_turn":
		return core.StopReasonStop
	default:
		if hasTools {
			return core.StopReasonToolUse
		}
		return core.StopReasonStop
	}
}

func normalizeModelError(result *core.ModelResult, err error) {
	if err == nil || result.StopReason != "" {
		return
	}
	if errors.Is(err, context.Canceled) {
		result.StopReason = core.StopReasonAborted
		return
	}
	result.StopReason = core.StopReasonError
}

func applyResponseMessage(result *core.ModelResult, message responseChatMessage, finishReason string) error {
	if message.Content != nil && strings.TrimSpace(*message.Content) != "" {
		result.Content = strings.TrimSpace(*message.Content)
		result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockText, Text: result.Content})
	}
	if strings.TrimSpace(message.ReasoningContent) != "" {
		result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockThinking, Thinking: &core.ThinkingBlock{Text: message.ReasoningContent}})
	}
	if len(message.ToolCalls) > 8 {
		result.ErrorCode = "PROVIDER_PROTOCOL_ERROR"
		result.StopReason = core.StopReasonError
		return &GatewayError{Code: result.ErrorCode, Message: "provider returned too many tool calls"}
	}
	for _, item := range message.ToolCalls {
		rawArguments := string(item.Function.Arguments)
		if len(rawArguments) > 1<<20 {
			result.ErrorCode = "PROVIDER_PROTOCOL_ERROR"
			result.StopReason = core.StopReasonError
			return &GatewayError{Code: result.ErrorCode, Message: "provider tool arguments exceed the size limit"}
		}
		arguments, repaired, parseErr := parseToolArguments(rawArguments)
		if parseErr != nil {
			result.ErrorCode = "PROVIDER_TOOL_ARGUMENTS_INVALID"
			detail := "provider returned malformed tool arguments"
			if toolArgumentsLookTruncated(rawArguments) {
				result.ErrorCode = "PROVIDER_TOOL_ARGUMENTS_TRUNCATED"
				detail = "provider returned truncated tool arguments"
			}
			result.StopReason = core.StopReasonError
			return &GatewayError{Code: result.ErrorCode, Message: detail}
		}
		if repaired {
			result.ToolArgumentRepairs++
		}
		callID := strings.TrimSpace(item.Id)
		if callID == "" {
			generated, generateErr := uuid.NewV7()
			if generateErr != nil {
				return generateErr
			}
			callID = generated.String()
		}
		call := core.ToolCall{ID: callID, Name: strings.TrimSpace(item.Function.Name), Arguments: arguments}
		result.ToolCalls = append(result.ToolCalls, call)
		result.ContentBlocks = append(result.ContentBlocks, core.ContentBlock{Type: core.ContentBlockToolCall, ToolCall: &core.ToolCallBlock{ID: call.ID, Name: call.Name, Arguments: call.Arguments}})
	}
	result.StopReason = mapStopReason(finishReason, len(result.ToolCalls) > 0)
	hasTextOrTool := result.Content != "" || len(result.ToolCalls) > 0
	if !hasTextOrTool {
		result.ErrorCode = "PROVIDER_EMPTY_RESPONSE"
		result.StopReason = core.StopReasonError
		return &GatewayError{Code: result.ErrorCode, Message: "provider returned no assistant content or tool calls"}
	}
	if result.Content == "" && len(result.ToolCalls) > 0 {
		result.Content = fmt.Sprintf("已生成 %d 个受控工具动作，等待策略执行与验收。", len(result.ToolCalls))
	}
	return nil
}

// chatFunctionArguments accepts both the OpenAI string form and providers
// that return an argument object directly. Validation still happens after the
// response is decoded so malformed or non-object values never reach a tool.
type chatFunctionArguments string

func (arguments *chatFunctionArguments) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*arguments = chatFunctionArguments(text)
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		*arguments = ""
		return nil
	}
	if json.Valid(data) {
		*arguments = chatFunctionArguments(string(bytes.TrimSpace(data)))
		return nil
	}
	return fmt.Errorf("invalid function arguments field")
}

func parseToolArguments(raw string) (map[string]any, bool, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if trimmed == "" {
		return map[string]any{}, true, nil
	}
	candidates := []string{trimmed}
	if unfenced, ok := stripJSONFence(trimmed); ok && unfenced != trimmed {
		candidates = append(candidates, unfenced)
	}
	var encoded string
	if json.Unmarshal([]byte(trimmed), &encoded) == nil && strings.TrimSpace(encoded) != "" {
		candidates = append(candidates, strings.TrimSpace(encoded))
	}
	for index, candidate := range candidates {
		if parsed, err := parseToolArgumentObject(candidate); err == nil {
			return parsed, index > 0, nil
		}
		repaired := repairJSONStringLiterals(candidate)
		if repaired != candidate {
			if parsed, err := parseToolArgumentObject(repaired); err == nil {
				return parsed, true, nil
			}
		}
	}
	return nil, false, fmt.Errorf("tool arguments are not one complete JSON object")
}

func parseToolArgumentObject(raw string) (map[string]any, error) {
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("tool arguments must be a JSON object")
	}
	return object, nil
}

func stripJSONFence(raw string) (string, bool) {
	if !strings.HasPrefix(raw, "```") || !strings.HasSuffix(raw, "```") {
		return raw, false
	}
	firstLine := strings.IndexByte(raw, '\n')
	if firstLine < 0 {
		return raw, false
	}
	return strings.TrimSpace(raw[firstLine+1 : len(raw)-3]), true
}

func toolArgumentsLookTruncated(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if unfenced, ok := stripJSONFence(trimmed); ok {
		trimmed = unfenced
	}
	return trimmed != "" && !strings.HasSuffix(trimmed, "}")
}

// repairJSONStringLiterals follows the conservative strategy used by pi:
// only raw control characters and invalid string escapes are repaired. It
// never invents missing braces, values, or fields for a side-effecting tool.
func repairJSONStringLiterals(raw string) string {
	var repaired strings.Builder
	repaired.Grow(len(raw) + 16)
	inString := false
	for index := 0; index < len(raw); index++ {
		character := raw[index]
		if !inString {
			repaired.WriteByte(character)
			if character == '"' {
				inString = true
			}
			continue
		}
		if character == '"' {
			repaired.WriteByte(character)
			inString = false
			continue
		}
		if character == '\\' {
			if index+1 >= len(raw) {
				repaired.WriteString(`\\`)
				continue
			}
			next := raw[index+1]
			if next == 'u' && index+5 < len(raw) && isHex4(raw[index+2:index+6]) {
				repaired.WriteString(raw[index : index+6])
				index += 5
				continue
			}
			if strings.ContainsRune(`"\\/bfnrt`, rune(next)) {
				repaired.WriteByte(character)
				repaired.WriteByte(next)
				index++
				continue
			}
			repaired.WriteString(`\\`)
			continue
		}
		if character < 0x20 {
			switch character {
			case '\b':
				repaired.WriteString(`\b`)
			case '\f':
				repaired.WriteString(`\f`)
			case '\n':
				repaired.WriteString(`\n`)
			case '\r':
				repaired.WriteString(`\r`)
			case '\t':
				repaired.WriteString(`\t`)
			default:
				_, _ = fmt.Fprintf(&repaired, `\u%04x`, character)
			}
			continue
		}
		repaired.WriteByte(character)
	}
	return repaired.String()
}

func isHex4(value string) bool {
	if len(value) != 4 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}

type chatTool struct {
	Type     string           `json:"type"`
	Function chatToolFunction `json:"function"`
}

type chatToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

func filterBuiltinTools(tools []chatTool, allowed []string) []chatTool {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[strings.TrimSpace(name)] = struct{}{}
	}
	filtered := make([]chatTool, 0, len(tools))
	for _, tool := range tools {
		if _, ok := allowedSet[tool.Function.Name]; ok {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

type modelList struct {
	Data []modelEntry `json:"data"`
}

type modelEntry struct {
	Id               string         `json:"id"`
	ToolCalling      *bool          `json:"tool_calling"`
	Vision           *bool          `json:"vision"`
	StructuredOutput *bool          `json:"structured_output"`
	Reasoning        *bool          `json:"reasoning"`
	ContextWindow    *int64         `json:"context_window"`
	Capabilities     map[string]any `json:"capabilities"`
}

func modelDescriptor(item modelEntry) core.ModelDescriptor {
	capabilities := item.Capabilities
	toolCalling, toolCallingKnown := capabilityBoolValue(item.ToolCalling, capabilities, "tool_calling")
	vision, _ := capabilityBoolValue(item.Vision, capabilities, "vision")
	structuredOutput, _ := capabilityBoolValue(item.StructuredOutput, capabilities, "structured_output")
	reasoning, _ := capabilityBoolValue(item.Reasoning, capabilities, "reasoning")
	return core.ModelDescriptor{
		ID:               strings.TrimSpace(item.Id),
		ToolCalling:      toolCalling,
		ToolCallingKnown: toolCallingKnown,
		Vision:           vision,
		StructuredOutput: structuredOutput,
		Reasoning:        reasoning,
		ContextWindow:    firstPositive(pointerInt64(item.ContextWindow), capabilityInt64(capabilities, "context_window")),
	}
}

func capabilityBoolValue(explicit *bool, values map[string]any, key string) (bool, bool) {
	if explicit != nil {
		return *explicit, true
	}
	value, ok := values[key].(bool)
	return value, ok
}

func capabilityInt64(values map[string]any, key string) int64 {
	value, ok := values[key].(float64)
	if !ok || value <= 0 {
		return 0
	}
	return int64(value)
}

func firstPositive(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func pointerInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func modelIDs(models []modelEntry) []string {
	candidates := make([]string, 0, len(models))
	for _, item := range models {
		id := strings.TrimSpace(item.Id)
		lower := strings.ToLower(id)
		if id == "" || strings.Contains(lower, "embedding") || strings.Contains(lower, "whisper") || strings.Contains(lower, "audio") || strings.Contains(lower, "image") {
			continue
		}
		candidates = append(candidates, id)
	}
	sort.Strings(candidates)
	return candidates
}

func chooseModel(models []modelEntry) string {
	candidates := modelIDs(models)
	if len(candidates) == 0 {
		return ""
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return modelScore(candidates[i]) > modelScore(candidates[j]) || (modelScore(candidates[i]) == modelScore(candidates[j]) && candidates[i] < candidates[j])
	})
	return candidates[0]
}

func modelScore(model string) int {
	lower := strings.ToLower(model)
	score := 0
	for _, marker := range []string{"gpt", "claude", "gemini", "qwen", "deepseek", "glm"} {
		if strings.Contains(lower, marker) {
			score += 10
		}
	}
	for _, marker := range []string{"mini", "nano", "flash", "haiku"} {
		if strings.Contains(lower, marker) {
			score -= 2
		}
	}
	return score
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

func safeProviderBody(reader io.Reader) string {
	// Consume a bounded amount so the transport can reuse the connection, but
	// never persist or expose provider error bodies: gateways sometimes echo
	// request fragments or credential-adjacent diagnostics.
	_, _ = io.Copy(io.Discard, io.LimitReader(reader, 8<<10))
	return "provider rejected the request; response details were redacted"
}

func estimateTokens(value string) int64 {
	runes := len([]rune(value))
	if runes == 0 {
		return 0
	}
	return int64((runes + 3) / 4)
}

func firstNonZero(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func usageFromChat(usage chatUsage) core.ModelUsage {
	contextTokens := firstNonZero(usage.PromptTokens, usage.InputTokens)
	cacheReadTokens := usage.PromptTokensDetails.CachedTokens
	inputTokens := contextTokens - cacheReadTokens
	if inputTokens < 0 {
		inputTokens = 0
	}
	return core.ModelUsage{
		InputTokens:           inputTokens,
		OutputTokens:          firstNonZero(usage.CompletionTokens, usage.OutputTokens),
		CachedTokens:          cacheReadTokens,
		CacheReadTokens:       cacheReadTokens,
		ContextTokens:         contextTokens,
		ReasoningTokens:       usage.CompletionTokensDetails.ReasoningTokens,
		UsageSemanticsVersion: "v2",
		Estimated:             usage.PromptTokens == 0 && usage.InputTokens == 0,
	}
}
