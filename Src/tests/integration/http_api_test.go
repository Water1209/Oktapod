package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

func TestHTTPProjectTaskAndEventContract(t *testing.T) {
	t.Parallel()

	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	service.WithModelCatalog(openaicompat.New("https://provider.invalid/v1", "", func() string { return "" }))
	server := httptest.NewServer(httpapi.NewTrustedHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	healthResponse, err := http.Get(server.URL + "/api/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = healthResponse.Body.Close()
	if healthResponse.Header.Get("Content-Security-Policy") == "" || healthResponse.Header.Get("X-Content-Type-Options") != "nosniff" || healthResponse.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("security headers missing: %#v", healthResponse.Header)
	}
	vaultResponse, err := http.Get(server.URL + "/api/v1/security/vault")
	if err != nil {
		t.Fatal(err)
	}
	_ = vaultResponse.Body.Close()
	if vaultResponse.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("vault response must not be cached: %#v", vaultResponse.Header)
	}
	diagnostics := getJSON[map[string]any](t, server.URL+"/api/v1/diagnostics")
	if diagnostics["status"] != "attention" || diagnostics["version"] == "" {
		t.Fatalf("diagnostics missing runtime status: %#v", diagnostics)
	}
	vault, ok := diagnostics["vault"].(map[string]any)
	if !ok || vault["state"] != "uninitialized" {
		t.Fatalf("diagnostics did not surface the uninitialized vault: %#v", diagnostics["vault"])
	}
	logPolicy, ok := diagnostics["logs"].(map[string]any)
	if !ok || logPolicy["available"] != false || logPolicy["redacted"] != true {
		t.Fatalf("diagnostics exposed an unsafe log policy: %#v", diagnostics["logs"])
	}
	maintenance, ok := diagnostics["maintenance"].(map[string]any)
	if !ok || maintenance["backup_endpoint_available"] != false || maintenance["restore_endpoint_available"] != false {
		t.Fatalf("diagnostics mixed privileged maintenance into health: %#v", diagnostics["maintenance"])
	}

	bootstrap := getJSON[map[string]any](t, server.URL+"/api/v1/bootstrap")
	if bootstrap["model_configured"] != false {
		t.Fatalf("bootstrap leaked or misreported credential state: %#v", bootstrap)
	}
	capabilities, ok := bootstrap["capabilities"].(map[string]any)
	if !ok || capabilities["network"] != "search-only-fixed-provider" || capabilities["command"] != "structured-no-shell" {
		t.Fatalf("runtime capability probe missing or overstated: %#v", bootstrap["capabilities"])
	}
	modelResponse, err := http.Get(server.URL + "/api/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	modelBody, _ := io.ReadAll(modelResponse.Body)
	_ = modelResponse.Body.Close()
	if modelResponse.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(modelBody), "MODEL_CREDENTIAL_MISSING") {
		t.Fatalf("missing-credential model catalog response status=%d body=%s", modelResponse.StatusCode, modelBody)
	}
	projectInput := map[string]any{"name": "HTTP project", "description": "contract test", "kind": "personal", "scene": "code", "default_model": "project-model", "fallback_model": "project-fallback"}
	project := postJSON[struct {
		ID            string `json:"id"`
		DefaultModel  string `json:"default_model"`
		FallbackModel string `json:"fallback_model"`
	}](t, server.URL+"/api/v1/projects", projectInput)
	if project.ID == "" {
		t.Fatal("project response has no id")
	}
	if project.DefaultModel != "project-model" || project.FallbackModel != "project-fallback" {
		t.Fatalf("project model policy = %#v", project)
	}
	updatedProject := patchJSONWithKey[struct {
		DefaultModel  string `json:"default_model"`
		FallbackModel string `json:"fallback_model"`
	}](t, server.URL+"/api/v1/projects/"+project.ID, map[string]any{"default_model": "updated-model", "fallback_model": "updated-fallback"}, "project-settings-key")
	if updatedProject.DefaultModel != "updated-model" || updatedProject.FallbackModel != "updated-fallback" {
		t.Fatalf("updated project model policy = %#v", updatedProject)
	}
	projectUsage := getJSON[struct {
		ProjectID  string `json:"project_id"`
		ModelCalls int64  `json:"model_calls"`
		ByModel    []any  `json:"by_model"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/usage")
	if projectUsage.ProjectID != project.ID || projectUsage.ModelCalls != 0 || len(projectUsage.ByModel) != 0 {
		t.Fatalf("empty project usage aggregate = %#v", projectUsage)
	}
	manualPlan := postJSONWithKey[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Source string `json:"source"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/plan-items", map[string]any{"title": "Independent project plan", "description": "must not mutate runtime", "visibility": "private"}, "create-plan-item-key")
	if manualPlan.ID == "" || manualPlan.Status != "todo" || manualPlan.Source != "manual" {
		t.Fatalf("manual plan item = %#v", manualPlan)
	}
	movedPlan := postJSONWithKey[struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}](t, server.URL+"/api/v1/plan-items/"+manualPlan.ID+"/status", map[string]any{"status": "in_progress"}, "move-plan-item-key")
	if movedPlan.ID != manualPlan.ID || movedPlan.Status != "in_progress" {
		t.Fatalf("moved plan item = %#v", movedPlan)
	}
	projectReplay := postJSON[struct {
		ID string `json:"id"`
	}](t, server.URL+"/api/v1/projects", projectInput)
	if projectReplay.ID != project.ID {
		t.Fatalf("project idempotency replay created a second project: %s != %s", projectReplay.ID, project.ID)
	}
	conflict := postProblem(t, server.URL+"/api/v1/projects", map[string]any{"name": "Different project", "description": "contract test", "kind": "personal", "scene": "code"})
	if conflict.Status != http.StatusConflict || conflict.Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("idempotency conflict = %#v", conflict)
	}

	taskInput := map[string]any{"goal": "Test HTTP flow", "mode": "answer", "visibility": "private"}
	task := postJSON[struct {
		ID             string `json:"id"`
		RunID          string `json:"run_id"`
		ConversationID string `json:"conversation_id"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/tasks", taskInput)
	if task.ID == "" || task.RunID == "" || task.ConversationID == "" {
		t.Fatalf("task response missing ids: %#v", task)
	}
	planBoard := getJSON[struct {
		Items []struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			LinkedTaskID string `json:"linked_task_id"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/plan-items")
	if len(planBoard.Items) != 2 || planBoard.Items[0].Status != "todo" || planBoard.Items[0].LinkedTaskID != task.ID || planBoard.Items[1].ID != manualPlan.ID || planBoard.Items[1].Status != "in_progress" {
		t.Fatalf("plan board = %#v", planBoard)
	}
	taskReplay := postJSON[struct {
		ID             string `json:"id"`
		RunID          string `json:"run_id"`
		ConversationID string `json:"conversation_id"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/tasks", taskInput)
	if taskReplay != task {
		t.Fatalf("task idempotency replay created a second run: %#v != %#v", taskReplay, task)
	}

	worker := okruntime.NewWorker("http-test-worker", repository, openaicompat.New("https://provider.invalid/v1", "", func() string { return "" }), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("worker result worked=%v err=%v", worked, err)
	}
	completed := getJSON[struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
	}](t, server.URL+"/api/v1/tasks/"+task.ID)
	if completed.Status != "blocked" || completed.ErrorCode != "MODEL_CREDENTIAL_MISSING" {
		t.Fatalf("completed task = %#v", completed)
	}
	inbox := getJSON[struct {
		Items []struct {
			Kind   string `json:"kind"`
			TaskID string `json:"task_id"`
			Reason string `json:"reason"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/inbox")
	if !slices.ContainsFunc(inbox.Items, func(item struct {
		Kind   string `json:"kind"`
		TaskID string `json:"task_id"`
		Reason string `json:"reason"`
	}) bool {
		return item.Kind == "task_recovery" && item.TaskID == task.ID &&
			strings.Contains(item.Reason, "模型配置或凭据") && !strings.Contains(item.Reason, "MODEL_CREDENTIAL_MISSING")
	}) {
		t.Fatalf("inbox did not aggregate blocked task: %#v", inbox.Items)
	}
	conversations := getJSON[struct {
		Items []struct {
			ID           string `json:"id"`
			MessageCount int64  `json:"message_count"`
			LastMessage  string `json:"last_message"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/conversations")
	if len(conversations.Items) != 1 || conversations.Items[0].ID != task.ConversationID || conversations.Items[0].MessageCount != 2 || !strings.Contains(conversations.Items[0].LastMessage, "MODEL_CREDENTIAL_MISSING") {
		t.Fatalf("conversations = %#v", conversations)
	}
	messages := getJSON[struct {
		Items []struct {
			Sequence int64  `json:"server_seq"`
			Role     string `json:"role"`
		} `json:"items"`
		NextCursor int64 `json:"next_cursor"`
	}](t, server.URL+"/api/v1/conversations/"+task.ConversationID+"/messages?after=1")
	if len(messages.Items) != 1 || messages.Items[0].Role != "assistant" || messages.Items[0].Sequence != 2 || messages.NextCursor != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	activity := getJSON[struct {
		Items []struct {
			TaskID    string `json:"task_id"`
			EventType string `json:"event_type"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/projects/"+project.ID+"/activity")
	if len(activity.Items) != 8 || activity.Items[0].TaskID != task.ID || activity.Items[0].EventType != "step.blocked" || activity.Items[len(activity.Items)-1].EventType != "plan.published" {
		t.Fatalf("activity = %#v", activity)
	}
	usage := getJSON[struct {
		ModelCalls  int64 `json:"model_calls"`
		InputTokens int64 `json:"input_tokens"`
	}](t, server.URL+"/api/v1/runs/"+task.RunID+"/usage")
	if usage.ModelCalls != 1 || usage.InputTokens != 0 {
		t.Fatalf("usage = %#v", usage)
	}
	acceptConflict := postProblemWithKey(t, server.URL+"/api/v1/tasks/"+task.ID+"/acceptance", map[string]any{"note": "not complete"}, "accept-blocked-key")
	if acceptConflict.Status != http.StatusConflict || acceptConflict.Code != "INVALID_STATE_TRANSITION" {
		t.Fatalf("accept blocked task = %#v", acceptConflict)
	}
	events := getJSON[struct {
		Items []struct {
			Sequence int64  `json:"stream_seq"`
			Type     string `json:"event_type"`
		} `json:"items"`
		NextCursor int64 `json:"next_cursor"`
	}](t, server.URL+"/api/v1/runs/"+task.RunID+"/events")
	if len(events.Items) != 8 || events.NextCursor != 8 || events.Items[7].Type != "step.blocked" {
		t.Fatalf("events = %#v", events)
	}

	streamContext, cancelStream := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelStream()
	streamRequest, err := http.NewRequestWithContext(streamContext, http.MethodGet, server.URL+"/api/v1/runs/"+task.RunID+"/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	streamRequest.Header.Set("Last-Event-ID", "7")
	streamResponse, err := http.DefaultClient.Do(streamRequest)
	if err != nil {
		t.Fatalf("open event stream: %v", err)
	}
	defer streamResponse.Body.Close()
	streamScanner := bufio.NewScanner(streamResponse.Body)
	var streamEvent map[string]any
	for streamScanner.Scan() {
		line := streamScanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &streamEvent); err != nil {
			t.Fatalf("decode SSE event: %v", err)
		}
		break
	}
	if streamEvent["event_type"] != "step.blocked" || streamEvent["stream_seq"] != float64(8) {
		t.Fatalf("SSE event does not follow OpenAPI JSON shape: %#v", streamEvent)
	}

	retried := postJSONWithKey[struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
	}](t, server.URL+"/api/v1/tasks/"+task.ID+"/commands", map[string]any{"command": "retry"}, "control-retry-key")
	if retried.Status != "queued" || retried.ErrorCode != "" {
		t.Fatalf("retried task = %#v", retried)
	}
	retriedReplay := postJSONWithKey[struct {
		Status string `json:"status"`
	}](t, server.URL+"/api/v1/tasks/"+task.ID+"/commands", map[string]any{"command": "retry"}, "control-retry-key")
	if retriedReplay.Status != "queued" {
		t.Fatalf("retry replay = %#v", retriedReplay)
	}
	if worked, err := worker.RunOnce(context.Background()); err != nil || !worked {
		t.Fatalf("recovery worker result worked=%v err=%v", worked, err)
	}
	reblocked := getJSON[struct {
		Status string `json:"status"`
	}](t, server.URL+"/api/v1/tasks/"+task.ID)
	if reblocked.Status != "blocked" {
		t.Fatalf("reblocked task = %#v", reblocked)
	}
	retriedAgain := postJSONWithKey[struct {
		Status    string `json:"status"`
		ErrorCode string `json:"error_code"`
	}](t, server.URL+"/api/v1/tasks/"+task.ID+"/commands", map[string]any{"command": "retry"}, "control-retry-again-key")
	if retriedAgain.Status != "queued" || retriedAgain.ErrorCode != "" {
		t.Fatalf("retried task = %#v", retriedAgain)
	}
	canceled := postJSONWithKey[struct {
		Status string `json:"status"`
	}](t, server.URL+"/api/v1/tasks/"+task.ID+"/commands", map[string]any{"command": "cancel"}, "control-cancel-key")
	if canceled.Status != "canceled" {
		t.Fatalf("canceled task = %#v", canceled)
	}
	followup := postJSONWithKey[struct {
		ID             string `json:"id"`
		ConversationID string `json:"conversation_id"`
	}](t, server.URL+"/api/v1/conversations/"+task.ConversationID+"/messages", map[string]any{"content": "继续说明失败原因", "mode": "answer", "visibility": "private"}, "conversation-followup-key")
	if followup.ID == "" || followup.ConversationID != task.ConversationID {
		t.Fatalf("conversation follow-up task=%#v", followup)
	}
	followupMessages := getJSON[struct {
		Items []struct {
			Sequence int64  `json:"server_seq"`
			Role     string `json:"role"`
			Content  string `json:"content"`
		} `json:"items"`
		NextCursor int64 `json:"next_cursor"`
	}](t, server.URL+"/api/v1/conversations/"+task.ConversationID+"/messages?after=3")
	if len(followupMessages.Items) != 1 || followupMessages.Items[0].Sequence != 4 || followupMessages.Items[0].Role != "user" || followupMessages.NextCursor != 4 {
		t.Fatalf("follow-up messages=%#v", followupMessages)
	}
	streamContext, cancelMessageStream := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelMessageStream()
	messageStreamRequest, err := http.NewRequestWithContext(streamContext, http.MethodGet, server.URL+"/api/v1/conversations/"+task.ConversationID+"/messages/stream?after=3", nil)
	if err != nil {
		t.Fatal(err)
	}
	messageStreamResponse, err := http.DefaultClient.Do(messageStreamRequest)
	if err != nil {
		t.Fatalf("open message stream: %v", err)
	}
	defer messageStreamResponse.Body.Close()
	if messageStreamResponse.StatusCode != http.StatusOK || messageStreamResponse.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("message stream status=%d content-type=%s", messageStreamResponse.StatusCode, messageStreamResponse.Header.Get("Content-Type"))
	}
	messageScanner := bufio.NewScanner(messageStreamResponse.Body)
	var messageEvent map[string]any
	for messageScanner.Scan() {
		line := messageScanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &messageEvent); err != nil {
			t.Fatalf("decode message SSE: %v", err)
		}
		break
	}
	if messageEvent["server_seq"] != float64(4) || messageEvent["role"] != "user" {
		t.Fatalf("message SSE event=%#v", messageEvent)
	}
}

type testModelCatalog struct {
	items []core.ModelDescriptor
}

func (c testModelCatalog) ListModels(context.Context) ([]core.ModelDescriptor, error) {
	return c.items, nil
}

func TestHTTPModelCatalogContract(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	service.WithModelCatalog(testModelCatalog{items: []core.ModelDescriptor{{ID: "gpt-test"}, {ID: "qwen-test"}}})
	server := httptest.NewServer(httpapi.NewTrustedHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	models := getJSON[struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/models")
	if len(models.Items) != 2 || models.Items[0].ID != "gpt-test" || models.Items[1].ID != "qwen-test" {
		t.Fatalf("models = %#v", models)
	}
}

func TestHTTPConfigurationDiagnosticsAndPricingWriteContract(t *testing.T) {
	repository, _ := newRepository(t)
	defer repository.Close()
	bootstrapRepository(t, repository)
	if _, err := repository.UpsertProviderInstance(context.Background(), testOrgID, core.UpsertProviderInstanceInput{
		ProviderID: "provider-pricing", DisplayName: "Pricing Provider", ProtocolFamily: core.ProviderProtocolOpenAICompatible,
		Endpoint: "https://provider.invalid/v1", CredentialRef: "model-provider/test/pricing", DefaultModelID: "model-pricing",
		Status: core.ProviderStatusActive, IsDefault: true,
	}); err != nil {
		t.Fatal(err)
	}
	service := application.NewService(repository, core.ProfilePersonal, "https://provider.invalid/v1", core.Actor{ID: testUserID, DisplayName: "Test User"}, core.Organization{ID: testOrgID, Name: "Test Org"}, func() string { return "" })
	server := httptest.NewServer(httpapi.NewTrustedHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()

	pricing := putJSON[struct {
		Items []struct {
			ProviderID string `json:"provider_id"`
			ModelID    string `json:"model_id"`
			UpdatedAt  string `json:"updated_at"`
		} `json:"items"`
	}](t, server.URL+"/api/v1/settings/model-pricing", map[string]any{"items": []map[string]any{{
		"provider_id": "provider-pricing", "model_id": "model-pricing", "currency": "USD",
		"input_per_1k": "0.001", "output_per_1k": "0.002", "effective_at": time.Now().UTC(), "version": 1,
	}}})
	if len(pricing.Items) != 1 || pricing.Items[0].ProviderID != "provider-pricing" || pricing.Items[0].ModelID != "model-pricing" || pricing.Items[0].UpdatedAt == "" {
		t.Fatalf("pricing response=%#v", pricing.Items)
	}
	diagnostics := getJSON[struct {
		CredentialIssues []struct {
			Kind       string `json:"kind"`
			Name       string `json:"name"`
			State      string `json:"state"`
			ActionPath string `json:"action_path"`
		} `json:"credential_issues"`
	}](t, server.URL+"/api/v1/diagnostics")
	if len(diagnostics.CredentialIssues) != 1 || diagnostics.CredentialIssues[0].Name != "Pricing Provider" || diagnostics.CredentialIssues[0].State != core.CredentialMissing || diagnostics.CredentialIssues[0].ActionPath != "/settings/providers" {
		t.Fatalf("credential issues=%#v", diagnostics.CredentialIssues)
	}
}

func getJSON[T any](t *testing.T, url string) T {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s status=%d body=%s", url, response.StatusCode, body)
	}
	var decoded T
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode GET %s: %v", url, err)
	}
	return decoded
}

func postJSON[T any](t *testing.T, url string, value any) T {
	return postJSONWithKey[T](t, url, value, "test-idempotency-key-123")
}

func postJSONWithKey[T any](t *testing.T, url string, value any, idempotencyKey string) T {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusAccepted {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("POST %s status=%d body=%s", url, response.StatusCode, responseBody)
	}
	var decoded T
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode POST %s: %v", url, err)
	}
	return decoded
}

func putJSON[T any](t *testing.T, url string, value any) T {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("PUT %s status=%d body=%s", url, response.StatusCode, responseBody)
	}
	var decoded T
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode PUT %s: %v", url, err)
	}
	return decoded
}

func patchJSONWithKey[T any](t *testing.T, url string, value any, idempotencyKey string) T {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("PATCH %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("PATCH %s status=%d body=%s", url, response.StatusCode, responseBody)
	}
	var decoded T
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode PATCH %s: %v", url, err)
	}
	return decoded
}

func deleteWithKey(t *testing.T, url, idempotencyKey string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("DELETE %s: %v", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("DELETE %s status=%d body=%s", url, response.StatusCode, responseBody)
	}
}

func postProblem(t *testing.T, url string, value any) struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
} {
	return postProblemWithKey(t, url, value, "test-idempotency-key-123")
}

func postProblemWithKey(t *testing.T, url string, value any, idempotencyKey string) struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
} {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var problem struct {
		Status int    `json:"status"`
		Code   string `json:"code"`
	}
	if err := json.NewDecoder(response.Body).Decode(&problem); err != nil {
		t.Fatal(err)
	}
	return problem
}
