package core

import "testing"

func TestHumanInputOptionalText(t *testing.T) {
	request := map[string]any{"questions": []any{map[string]any{"id": "answer", "question": "是否继续", "required": true, "allow_freeform": true, "options": []any{map[string]any{"id": "continue", "label": "继续"}}}}}
	for _, value := range []any{
		map[string]any{"selected": []any{"continue"}, "text": ""},
		map[string]any{"selected": []any{"continue"}, "text": "只做基础版"},
		map[string]any{"selected": []any{}, "text": "调整方案"},
	} {
		if err := ValidateHumanInputAnswer(request, map[string]any{"answer": value}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateHumanInputAnswer(request, map[string]any{"answer": map[string]any{"selected": []any{}, "text": " "}}); err == nil {
		t.Fatal("empty required answer accepted")
	}
}

func TestValidateHumanInputRequest(t *testing.T) {
	request := map[string]any{"questions": []any{map[string]any{
		"id": "scope", "question": "选择范围", "selection_mode": "multiple", "required": true,
		"options": []any{map[string]any{"id": "a", "label": "A"}, map[string]any{"id": "b", "label": "B"}},
	}}}
	if err := ValidateHumanInputRequest(request); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if err := ValidateHumanInputRequest(map[string]any{"questions": []any{map[string]any{"id": "x", "question": "x"}, map[string]any{"id": "x", "question": "x"}}}); err == nil {
		t.Fatal("duplicate question ids accepted")
	}
}

func TestValidateHumanInputAnswer(t *testing.T) {
	request := map[string]any{"questions": []any{map[string]any{
		"id": "scope", "question": "选择范围", "selection_mode": "single", "required": true,
		"options": []any{map[string]any{"id": "a", "label": "A"}, map[string]any{"id": "b", "label": "B"}},
	}}}
	if err := ValidateHumanInputAnswer(request, map[string]any{"scope": []any{"a"}}); err != nil {
		t.Fatalf("valid answer rejected: %v", err)
	}
	if err := ValidateHumanInputAnswer(request, map[string]any{"scope": []any{"a", "b"}}); err == nil {
		t.Fatal("multiple values accepted for single choice")
	}
	if err := ValidateHumanInputAnswer(request, map[string]any{"scope": []any{"unknown"}}); err == nil {
		t.Fatal("unknown option accepted")
	}
}
