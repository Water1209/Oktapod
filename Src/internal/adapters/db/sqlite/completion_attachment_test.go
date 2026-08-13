package sqlite

import (
	"reflect"
	"testing"
)

func TestBinaryContextAssetIDsRequireStagingOnlyForNonTextAttachments(t *testing.T) {
	snapshot := `{"context_asset_snapshots":[
		{"id":"pdf-id","name":"training.pdf","media_type":"application/pdf"},
		{"id":"markdown-id","name":"notes.md","media_type":"text/markdown"},
		{"id":"image-id","name":"diagram.png","media_type":"image/png"}
	]}`
	want := []string{"pdf-id", "image-id"}
	if got := binaryContextAssetIDs(snapshot); !reflect.DeepEqual(got, want) {
		t.Fatalf("binary context assets=%#v want=%#v", got, want)
	}
}
