package application

import "testing"

func TestBranchSummaryUsesIndependentFiveSectionContract(t *testing.T) {
	valid := `## Branch Intent
Continue the tree work.

## Completed
- Migration added.

## Key Decisions
- **Independent format**: Do not reuse compaction.

## Open Items
- Browser UI.

## Handoff Context
- session_entries.entry_seq`
	if !validBranchSummary(valid) {
		t.Fatal("valid branch summary rejected")
	}
	if validConversationCompactionSummary(valid) {
		t.Fatal("branch summary was accepted as a six-section compaction")
	}
	if validBranchSummary(testCompactionSummary) {
		t.Fatal("conversation compaction was accepted as a branch summary")
	}
}
