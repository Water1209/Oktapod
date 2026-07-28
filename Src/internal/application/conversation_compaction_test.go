package application

import (
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const testCompactionSummary = `## Goal
- Continue the task.

## Constraints & Preferences
- Keep exact paths.

## Progress
### Done
- [x] Read the baseline.

### In Progress
- [ ] Implement the change.

### Blocked
- (none)

## Key Decisions
- **Typed history**: Preserve message roles.

## Next Steps
1. Run tests.

## Critical Context
- Src/internal/application`

func TestConversationCompactionPromptUsesInitialContract(t *testing.T) {
	prompt := conversationCompactionPrompt("", []core.Message{{Role: "user", Content: "continue"}}, core.ConversationFileContext{}, "")
	if strings.Contains(prompt, "<previous-summary>") {
		t.Fatalf("initial prompt unexpectedly contains previous summary: %s", prompt)
	}
	if !strings.Contains(prompt, conversationCompactionInitialPrompt) || strings.Contains(prompt, conversationCompactionUpdatePrompt) {
		t.Fatalf("initial compaction contract missing: %s", prompt)
	}
}

func TestConversationCompactionPromptUsesIncrementalContract(t *testing.T) {
	prompt := conversationCompactionPrompt(testCompactionSummary, []core.Message{{Role: "assistant", Content: "done"}}, core.ConversationFileContext{ReadFiles: []string{"README.md"}, ModifiedFiles: []string{"Src/main.go"}}, "retain receipts")
	for _, expected := range []string{
		"<previous-summary>",
		testCompactionSummary,
		conversationCompactionUpdatePrompt,
		"<read-files>\nREADME.md\n</read-files>",
		"<modified-files>\nSrc/main.go\n</modified-files>",
		"<additional-instructions>\nretain receipts\n</additional-instructions>",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("incremental compaction prompt missing %q: %s", expected, prompt)
		}
	}
}

func TestValidConversationCompactionSummaryRequiresExactOrderedSections(t *testing.T) {
	if !validConversationCompactionSummary(testCompactionSummary) {
		t.Fatal("valid six-section summary was rejected")
	}
	if validConversationCompactionSummary("## Goal\nx\n## Progress\nx\n## Constraints & Preferences\nx\n## Key Decisions\nx\n## Next Steps\nx\n## Critical Context\nx") {
		t.Fatal("out-of-order summary was accepted")
	}
	if validConversationCompactionSummary(strings.Replace(testCompactionSummary, "## Critical Context", "## Context", 1)) {
		t.Fatal("summary without the exact final section was accepted")
	}
}
