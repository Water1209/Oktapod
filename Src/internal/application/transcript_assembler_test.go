package application

import (
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestTranscriptAssemblerPreservesHumanAnswer(t *testing.T) {
	messages := (TranscriptAssembler{}).Run("先确认", []map[string]any{
		{"kind": "assistant", "content": "是否继续？"},
		{"kind": "user", "content": "继续；补充说明为空"},
	}, "按回答继续")
	if len(messages) != 4 || messages[2].Role != core.MessageRoleUser || messages[2].Content[0].Text != "继续；补充说明为空" {
		t.Fatalf("human answer missing from model transcript: %#v", messages)
	}
}

func TestTranscriptAssemblerPreservesConversationRoles(t *testing.T) {
	assembler := TranscriptAssembler{}
	messages := assembler.Conversation("summary", []core.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "answer"},
	}, "next")
	if len(messages) != 4 || messages[0].Role != core.MessageRoleUser || messages[1].Role != core.MessageRoleUser || messages[2].Role != core.MessageRoleAssistant || messages[3].Role != core.MessageRoleUser {
		t.Fatalf("messages=%#v", messages)
	}
	if messages[0].Content[0].Text != "<conversation-summary>\nsummary\n</conversation-summary>" {
		t.Fatalf("summary=%#v", messages[0])
	}
}

func TestTranscriptAssemblerKeepsToolCallResultPaired(t *testing.T) {
	assembler := TranscriptAssembler{}
	messages := assembler.Run("goal", []map[string]any{
		{"kind": "assistant", "content": "I will inspect"},
		{"kind": "tool_result", "tool_call_id": "call-1", "tool_name": "read_file", "arguments": map[string]any{"path": "README.md"}, "status": core.StatusSucceeded, "output": map[string]any{"content": "body"}},
	}, "continue")
	if len(messages) != 5 {
		t.Fatalf("messages=%#v", messages)
	}
	call := messages[2].Content[0].ToolCall
	result := messages[3].Content[0].ToolResult
	if messages[2].Role != core.MessageRoleAssistant || call == nil || call.ID != "call-1" {
		t.Fatalf("tool call=%#v", messages[2])
	}
	if messages[3].Role != core.MessageRoleTool || result == nil || result.ToolCallID != call.ID || result.IsError {
		t.Fatalf("tool result=%#v", messages[3])
	}
	if messages[4].Role != core.MessageRoleUser {
		t.Fatalf("continuation=%#v", messages[4])
	}
}

func TestTranscriptAssemblerPreservesReasoningOnTheAssistantToolCall(t *testing.T) {
	messages := (TranscriptAssembler{}).Run("goal", []map[string]any{
		{"kind": "assistant", "step_id": "step-1", "group_id": "step-1", "content": "", "reasoning_content": "inspect before acting"},
		{"kind": "tool_result", "group_id": "step-1", "tool_call_id": "call-1", "tool_name": "read_file", "arguments": map[string]any{"path": "README.md"}, "status": core.StatusSucceeded, "output": map[string]any{"content": "body"}},
	}, "")
	if len(messages) != 3 {
		t.Fatalf("messages=%#v", messages)
	}
	assistant := messages[1]
	if assistant.Role != core.MessageRoleAssistant || len(assistant.Content) != 2 || assistant.Content[0].Thinking == nil || assistant.Content[0].Thinking.Text != "inspect before acting" || assistant.Content[1].ToolCall == nil {
		t.Fatalf("reasoning/tool call not preserved together: %#v", assistant)
	}
	if messages[2].Role != core.MessageRoleTool || messages[2].ToolCallID != "call-1" {
		t.Fatalf("tool result=%#v", messages[2])
	}
}

func TestTranscriptAssemblerDropsOrphanToolResult(t *testing.T) {
	messages := (TranscriptAssembler{}).Run("goal", []map[string]any{{"kind": "tool_result", "tool_name": "read_file"}}, "")
	if len(messages) != 1 {
		t.Fatalf("orphan result leaked: %#v", messages)
	}
}

func TestTranscriptAssemblerKeepsToolPairsAcrossRecoveryOutcomes(t *testing.T) {
	for _, status := range []string{core.StatusSucceeded, core.StatusFailed, core.StatusCanceled, core.StatusBlocked, "unknown"} {
		t.Run(status, func(t *testing.T) {
			messages := (TranscriptAssembler{}).Run("goal", []map[string]any{
				{"kind": "assistant", "step_id": "step-1", "content": "inspect"},
				{"kind": "tool_result", "group_id": "step-1", "tool_call_id": "call-1", "tool_name": "read_file", "arguments": map[string]any{"path": "README.md"}, "status": status, "error_code": "RECOVERY_RESULT", "output": map[string]any{}},
			}, "continue")
			assertModelToolPairs(t, messages)
		})
	}
}

func TestTranscriptAssemblerNormalizesCompactedAndRolledBackToolContext(t *testing.T) {
	conversation := []core.ModelMessage{
		{Role: core.MessageRoleAssistant, Content: []core.ContentBlock{
			{Type: core.ContentBlockText, Text: "preserve text"},
			{Type: core.ContentBlockToolCall, ToolCall: &core.ToolCallBlock{ID: "paired", Name: "read_file", Arguments: map[string]any{"path": "README.md"}}},
			{Type: core.ContentBlockToolCall, ToolCall: &core.ToolCallBlock{ID: "truncated", Name: "write_file", Arguments: map[string]any{}}},
		}},
		{Role: core.MessageRoleTool, ToolCallID: "paired", ToolName: "read_file", Content: []core.ContentBlock{{Type: core.ContentBlockToolResult, ToolResult: &core.ToolResultBlock{ToolCallID: "paired", ToolName: "read_file", Content: "ok"}}}},
		{Role: core.MessageRoleTool, ToolCallID: "rolled-back", ToolName: "write_file", Content: []core.ContentBlock{{Type: core.ContentBlockToolResult, ToolResult: &core.ToolResultBlock{ToolCallID: "rolled-back", ToolName: "write_file", Content: "unknown", IsError: true}}}},
	}
	messages := (TranscriptAssembler{}).RunWithConversation(conversation, "goal", nil, "")
	assertModelToolPairs(t, messages)
	if len(messages) != 3 || messages[0].Role != core.MessageRoleAssistant || len(messages[0].Content) != 2 {
		t.Fatalf("normalized conversation=%#v", messages)
	}
	if messages[0].Content[0].Text != "preserve text" || messages[0].Content[1].ToolCall == nil || messages[0].Content[1].ToolCall.ID != "paired" {
		t.Fatalf("assistant content=%#v", messages[0])
	}
}

func assertModelToolPairs(t *testing.T, messages []core.ModelMessage) {
	t.Helper()
	pending := make(map[string]struct{})
	for _, message := range messages {
		if message.Role == core.MessageRoleAssistant {
			for _, block := range message.Content {
				if block.Type == core.ContentBlockToolCall && block.ToolCall != nil {
					if _, exists := pending[block.ToolCall.ID]; exists {
						t.Fatalf("duplicate tool call %q in %#v", block.ToolCall.ID, messages)
					}
					pending[block.ToolCall.ID] = struct{}{}
				}
			}
		}
		if message.Role == core.MessageRoleTool {
			if _, exists := pending[message.ToolCallID]; !exists {
				t.Fatalf("orphan tool result %q in %#v", message.ToolCallID, messages)
			}
			delete(pending, message.ToolCallID)
		}
	}
	if len(pending) != 0 {
		t.Fatalf("orphan tool calls=%#v in %#v", pending, messages)
	}
}

func TestTranscriptAssemblerCarriesConversationIntoNewRun(t *testing.T) {
	assembler := TranscriptAssembler{}
	conversation := assembler.Conversation("", []core.Message{
		{Role: "user", Content: "生成报告"},
		{Role: "assistant", Content: "PROVIDER_AUTH"},
	}, "")
	messages := assembler.RunWithConversation(conversation, "继续当前任务", nil, "")
	if len(messages) != 3 {
		t.Fatalf("messages=%#v", messages)
	}
	if messages[0].Content[0].Text != "生成报告" || messages[1].Content[0].Text != "PROVIDER_AUTH" {
		t.Fatalf("conversation context lost: %#v", messages)
	}
	if messages[2].Role != core.MessageRoleUser || messages[2].Content[0].Text != "继续当前任务" {
		t.Fatalf("current goal not appended after history: %#v", messages[2])
	}
}
