package application

import (
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

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

func TestTranscriptAssemblerDropsOrphanToolResult(t *testing.T) {
	messages := (TranscriptAssembler{}).Run("goal", []map[string]any{{"kind": "tool_result", "tool_name": "read_file"}}, "")
	if len(messages) != 1 {
		t.Fatalf("orphan result leaked: %#v", messages)
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
