package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type fakeContextRepository struct {
	settings core.ConversationContextSettings
}

func (f *fakeContextRepository) GetConversationContextSettings(context.Context, core.Scope) (core.ConversationContextSettings, error) {
	return f.settings, nil
}

func (f *fakeContextRepository) UpsertConversationContextSettings(_ context.Context, scope core.Scope, input core.UpdateConversationContextSettingsInput) (core.ConversationContextSettings, error) {
	f.settings = core.ConversationContextSettings{OrganizationID: scope.OrganizationID, OwnerUserID: scope.UserID, AutoCompact: input.AutoCompact, TriggerTokens: input.TriggerTokens, RetainTokens: input.RetainTokens, SummaryMaxTokens: input.SummaryMaxTokens}
	return f.settings, nil
}

func TestContextConfigurePersistsAllBudgets(t *testing.T) {
	repository := &fakeContextRepository{settings: core.ConversationContextSettings{AutoCompact: true, TriggerTokens: 100000, RetainTokens: 20000, SummaryMaxTokens: 4096}}
	var output bytes.Buffer
	err := runContextConfigure(t.Context(), repository, core.Scope{OrganizationID: "org", UserID: "user"}, []string{"--auto=false", "--trigger-tokens=80000", "--retain-tokens=16000", "--summary-tokens=2048"}, &output)
	if err != nil {
		t.Fatal(err)
	}
	if repository.settings.AutoCompact || repository.settings.TriggerTokens != 80000 || repository.settings.RetainTokens != 16000 || repository.settings.SummaryMaxTokens != 2048 {
		t.Fatalf("settings=%#v", repository.settings)
	}
	if !strings.Contains(output.String(), "立即生效") || !strings.Contains(output.String(), "80000 Token") {
		t.Fatalf("output=%q", output.String())
	}
}
