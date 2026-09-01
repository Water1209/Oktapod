package integration_test

import (
	"encoding/json"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// transitionPlanToolCall keeps unrelated integration fixtures honest under
// transition_v2: the first business-tool response must publish a semantic,
// user-facing plan in the same model response. Tests dedicated to plan repair
// continue to build their own invalid and recovery responses explicitly.
func transitionPlanToolCall(id, title, description string) map[string]any {
	arguments, _ := json.Marshal(map[string]any{
		"items": []map[string]any{{
			"title":       title,
			"description": description,
		}},
	})
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      core.ToolReplacePlan,
			"arguments": string(arguments),
		},
	}
}
