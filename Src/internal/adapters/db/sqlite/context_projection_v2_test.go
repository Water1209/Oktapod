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

func TestTokenProjectionAlwaysRetainsLatestGroup(t *testing.T) {
	history := []map[string]any{
		{"kind": "assistant", "group_id": "old", "position": int64(1), "content": "first"},
		{"kind": "tool_result", "group_id": "old", "position": int64(2), "tool_name": core.ToolWebSearch, "status": core.StatusSucceeded, "output": map[string]any{"result": "first"}},
		{"kind": "assistant", "group_id": "new", "position": int64(3), "content": strings.Repeat("latest ", 4000), "reasoning_content": "latest reasoning"},
		{"kind": "tool_result", "group_id": "new", "position": int64(4), "tool_name": core.ToolWebSearch, "status": core.StatusSucceeded, "output": map[string]any{"result": strings.Repeat("latest ", 4000)}},
	}

	projected, checkpoint, _ := projectContinuationHistoryV2(history, 1)
	if len(projected) != 2 {
		t.Fatalf("latest group was dropped: items=%d", len(projected))
	}
	if checkpoint == nil || checkpoint["dropped_groups"] != 1 {
		t.Fatalf("checkpoint=%#v", checkpoint)
	}
	for _, item := range projected {
		if group, _ := item["group_id"].(string); group != "new" {
			t.Fatalf("old group survived: %#v", item)
		}
	}

	fallback, _, _ := projectContinuationHistoryV2(history, 0)
	if len(fallback) < 2 {
		t.Fatalf("byte fallback dropped the latest group: items=%d", len(fallback))
	}
	if group, _ := fallback[len(fallback)-1]["group_id"].(string); group != "new" {
		t.Fatalf("byte fallback ended on group=%q", group)
	}
}
