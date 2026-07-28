package main

import (
	"context"
	"errors"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type fakeModelRepository struct {
	config core.ModelProviderConfig
	err    error
}

func (f *fakeModelRepository) GetModelProviderConfig(context.Context, string) (core.ModelProviderConfig, error) {
	return f.config, f.err
}

func (f *fakeModelRepository) UpsertModelProviderConfig(_ context.Context, organizationID string, input core.UpsertModelProviderConfigInput) (core.ModelProviderConfig, error) {
	if f.err != nil {
		return core.ModelProviderConfig{}, f.err
	}
	f.config = core.ModelProviderConfig{OrganizationID: organizationID, Endpoint: input.Endpoint, DefaultModel: input.DefaultModel, SecretRef: input.SecretRef}
	return f.config, nil
}
func (f *fakeModelRepository) DeleteModelProviderConfig(context.Context, string) error {
	f.config = core.ModelProviderConfig{}
	return f.err
}

type fakeModelSecrets struct {
	values map[string]string
}

func (f *fakeModelSecrets) Get(_ context.Context, ref string) (string, error) {
	value, ok := f.values[ref]
	if !ok {
		return "", errors.New("not found")
	}
	return value, nil
}
func (f *fakeModelSecrets) Set(_ context.Context, ref, value string) error {
	f.values[ref] = value
	return nil
}
func (f *fakeModelSecrets) Delete(_ context.Context, ref string) error {
	delete(f.values, ref)
	return nil
}

func TestValidateModelEndpoint(t *testing.T) {
	if got, err := validateModelEndpoint("https://api.example.test/v1/"); err != nil || got != "https://api.example.test/v1" {
		t.Fatalf("endpoint=%q err=%v", got, err)
	}
	for _, value := range []string{"", "file:///tmp/model", "https://user:secret@example.test/v1", "https://example.test/v1#token"} {
		if _, err := validateModelEndpoint(value); err == nil {
			t.Fatalf("endpoint %q must be rejected", value)
		}
	}
}

func TestConfigureRollbackRestoresCredentialWhenDatabaseWriteFails(t *testing.T) {
	ctx := context.Background()
	ref := "model-provider/org-1/test-id"
	repository := &fakeModelRepository{err: errors.New("database unavailable")}
	secrets := &fakeModelSecrets{values: map[string]string{ref: "old-secret"}}
	err := saveModelConfig(ctx, repository, secrets, "org-1", ref, "https://example.test/v1", "model", "new-secret")
	if err == nil {
		t.Fatal("database failure was expected")
	}
	if got, _ := secrets.Get(ctx, ref); got != "old-secret" {
		t.Fatalf("credential=%q", got)
	}
}
