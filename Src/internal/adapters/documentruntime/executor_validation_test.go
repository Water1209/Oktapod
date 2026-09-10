package documentruntime

import "testing"

func TestValidateCreatedDocumentRejectsBlankPDFForTextSpecification(t *testing.T) {
	specification := map[string]any{
		"title":    "会议摘要",
		"sections": []any{map[string]any{"heading": "结论", "bullets": []any{"需要整改"}}},
	}
	blank := map[string]any{"units": []any{map[string]any{"text": "   "}}}
	if err := validateCreatedDocument("pdf", specification, blank); documentErrorCode(err) != "DOCUMENT_CONTENT_EMPTY" {
		t.Fatalf("blank PDF validation error=%v", err)
	}
	readable := map[string]any{"units": []any{map[string]any{"text": "会议摘要\n需要整改"}}}
	if err := validateCreatedDocument("pdf", specification, readable); err != nil {
		t.Fatalf("readable PDF rejected: %v", err)
	}
}

func TestValidateCreatedDocumentLeavesOtherFormatsUnchanged(t *testing.T) {
	if err := validateCreatedDocument("docx", map[string]any{"title": "标题"}, map[string]any{}); err != nil {
		t.Fatalf("DOCX validation unexpectedly failed: %v", err)
	}
}
