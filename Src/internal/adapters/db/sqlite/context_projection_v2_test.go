package sqlite

import (
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestTokenProjectionDropsOldReasoningByWholeGroup(t *testing.T) {
	history := make([]map[string]any, 0, 20)
	for index := 0; index < 10; index++ {
		group := "group-" + string(rune('a'+index))
		history = append(history,
			map[string]any{"kind": "assistant", "group_id": group, "position": int64(index*2 + 1), "content": "working", "reasoning_content": strings.Repeat("private reasoning ", 40)},
			map[string]any{"kind": "tool_result", "group_id": group, "position": int64(index*2 + 2), "tool_name": core.ToolWebSearch, "status": core.StatusSucceeded, "output": map[string]any{"content_hash": group}},
		)
	}
	projected, checkpoint, _ := projectContinuationHistoryV2(history, 420)
	if checkpoint == nil || len(projected) == len(history) {
		t.Fatalf("projection did not compact: items=%d checkpoint=%#v", len(projected), checkpoint)
	}
	latestReasoning := false
	for _, item := range projected {
		group, _ := item["group_id"].(string)
		_, hasReasoning := item["reasoning_content"]
		if group == "group-j" && hasReasoning {
			latestReasoning = true
		}
		if group != "group-j" && hasReasoning {
			t.Fatalf("old reasoning survived projection: %#v", item)
		}
	}
	if !latestReasoning {
		t.Fatal("latest provider reasoning group was not retained")
	}
}
