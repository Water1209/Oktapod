package sqlite

import (
	"reflect"
	"testing"
)

func TestBinaryContextAssetIDsRequireStagingOnlyForNonTextAttachments(t *testing.T) {
	snapshot := `{"context_asset_snapshots":[
		{"id":"pdf-id","name":"training.pdf","media_type":"application/pdf"},
		{"id":"pptx-id","name":"slides.pptx","media_type":"application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{"id":"docx-id","name":"notes.docx","media_type":"application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"id":"xlsx-id","name":"plan.xlsx","media_type":"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"id":"markdown-id","name":"notes.md","media_type":"text/markdown"},
		{"id":"xml-id","name":"feed.xml","media_type":"application/xml"},
		{"id":"image-id","name":"diagram.png","media_type":"image/png"}
	]}`
	want := []string{"pdf-id", "pptx-id", "docx-id", "xlsx-id", "image-id"}
	if got := binaryContextAssetIDs(snapshot); !reflect.DeepEqual(got, want) {
		t.Fatalf("binary context assets=%#v want=%#v", got, want)
	}
}
