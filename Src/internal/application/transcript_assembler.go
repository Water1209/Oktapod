package application

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// TranscriptAssembler is the only component allowed to convert persisted
// conversation/run history into provider-neutral model messages.
type TranscriptAssembler struct{}

func (TranscriptAssembler) Conversation(summary string, messages []core.Message, pendingContent string) []core.ModelMessage {
	result := make([]core.ModelMessage, 0, len(messages)+2)
	if strings.TrimSpace(summary) != "" {
		result = append(result, textModelMessage(core.MessageRoleUser, "<conversation-summary>\n"+strings.TrimSpace(summary)+"\n</conversation-summary>"))
	}
	for _, message := range messages {
		role := core.MessageRole(message.Role)
		if role != core.MessageRoleUser && role != core.MessageRoleAssistant && role != core.MessageRoleTool {
			continue
		}
		result = append(result, textModelMessage(role, message.Content))
	}
	if strings.TrimSpace(pendingContent) != "" {
		result = append(result, textModelMessage(core.MessageRoleUser, strings.TrimSpace(pendingContent)))
	}
	return result
}

func (TranscriptAssembler) Run(initialUserPrompt string, history []map[string]any, continuationInstruction string) []core.ModelMessage {
	return (TranscriptAssembler{}).RunWithConversation(nil, initialUserPrompt, history, continuationInstruction)
}

// RunWithConversation projects the compacted conversation branch before the
// current Run goal, then appends durable same-Run model/tool history. This is
// the task equivalent of Pi's compaction-aware session context: a new Run does
// not forget the Thread that created it, while tool continuation remains
// checkpoint-scoped to that Run.
func (TranscriptAssembler) RunWithConversation(conversation []core.ModelMessage, initialUserPrompt string, history []map[string]any, continuationInstruction string) []core.ModelMessage {
	result := make([]core.ModelMessage, 0, len(conversation)+len(history)*2+2)
	for _, message := range conversation {
		if message.Role != core.MessageRoleUser && message.Role != core.MessageRoleAssistant && message.Role != core.MessageRoleTool {
			continue
		}
		result = append(result, message)
	}
	result = append(result, textModelMessage(core.MessageRoleUser, initialUserPrompt))
	for _, item := range history {
		kind, _ := item["kind"].(string)
		switch kind {
		case "assistant":
			content, _ := item["content"].(string)
			if strings.TrimSpace(content) != "" {
				result = append(result, textModelMessage(core.MessageRoleAssistant, content))
			}
		case "tool_result":
			callID, _ := item["tool_call_id"].(string)
			toolName, _ := item["tool_name"].(string)
			arguments, _ := item["arguments"].(map[string]any)
			if callID == "" || toolName == "" {
				continue
			}
			result = append(result, core.ModelMessage{Role: core.MessageRoleAssistant, Content: []core.ContentBlock{{
				Type: core.ContentBlockToolCall,
				ToolCall: &core.ToolCallBlock{
					ID:        callID,
					Name:      toolName,
					Arguments: arguments,
				},
			}}})
			result = append(result, core.ModelMessage{Role: core.MessageRoleTool, ToolCallID: callID, ToolName: toolName, Content: []core.ContentBlock{{
				Type: core.ContentBlockToolResult,
				ToolResult: &core.ToolResultBlock{
					ToolCallID: callID,
					ToolName:   toolName,
					Content:    renderToolResult(item),
					IsError:    toolResultFailed(item),
				},
			}}})
		}
	}
	if strings.TrimSpace(continuationInstruction) != "" {
		result = append(result, textModelMessage(core.MessageRoleUser, continuationInstruction))
	}
	return result
}

func textModelMessage(role core.MessageRole, content string) core.ModelMessage {
	return core.ModelMessage{Role: role, Content: []core.ContentBlock{{Type: core.ContentBlockText, Text: content}}}
}

func renderToolResult(item map[string]any) string {
	payload := map[string]any{}
	for _, key := range []string{"status", "error_code", "output"} {
		if value, ok := item[key]; ok {
			payload[key] = value
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprint(payload)
	}
	return string(body)
}

func toolResultFailed(item map[string]any) bool {
	status, _ := item["status"].(string)
	return status != "" && status != core.StatusSucceeded
}

func RenderLegacyTranscript(messages []core.ModelMessage) string {
	var value strings.Builder
	for _, message := range messages {
		for _, block := range message.Content {
			switch block.Type {
			case core.ContentBlockText:
				value.WriteString(string(message.Role))
				value.WriteString(": ")
				value.WriteString(block.Text)
				value.WriteByte('\n')
			case core.ContentBlockToolCall:
				if block.ToolCall != nil {
					value.WriteString("assistant tool_call ")
					value.WriteString(block.ToolCall.Name)
					value.WriteString(": ")
					encoded, _ := json.Marshal(block.ToolCall.Arguments)
					value.Write(encoded)
					value.WriteByte('\n')
				}
			case core.ContentBlockToolResult:
				if block.ToolResult != nil {
					value.WriteString("tool ")
					value.WriteString(block.ToolResult.ToolName)
					value.WriteString(": ")
					value.WriteString(block.ToolResult.Content)
					value.WriteByte('\n')
				}
			}
		}
	}
	return value.String()
}
