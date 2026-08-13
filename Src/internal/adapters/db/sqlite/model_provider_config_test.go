package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func TestModelProviderConfigPersistsNonSecretSettings(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "oktapod.db"), filepath.Join(root, "workspaces"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actor := core.Actor{ID: "00000000-0000-7000-8000-000000000001", DisplayName: "本地用户"}
	organization := core.Organization{ID: "00000000-0000-7000-8000-000000000002", Name: "个人空间"}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		t.Fatal(err)
	}

	want := core.UpsertModelProviderConfigInput{
		Endpoint: "https://api.example.test/v1/", DefaultModel: "example-model", SecretRef: "model-provider/test/primary",
	}
	got, err := store.UpsertModelProviderConfig(ctx, organization.ID, want)
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != "https://api.example.test/v1" || got.DefaultModel != want.DefaultModel || got.SecretRef != want.SecretRef {
		t.Fatalf("config=%+v", got)
	}
	models, err := store.ListProviderModelOptions(ctx, organization.ID, "legacy-default-"+organization.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || !models[0].Enabled || !models[0].Default || models[0].ModelID != want.DefaultModel {
		t.Fatalf("enabled models=%+v", models)
	}
	var columns int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('model_provider_configs') WHERE name IN ('api_key', 'credential', 'secret_value')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if columns != 0 {
		t.Fatal("model provider table must not contain plaintext credential columns")
	}
}
