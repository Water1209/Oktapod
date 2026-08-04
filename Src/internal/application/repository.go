package application

import (
	"context"
	"io"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// Repository is the application composition root. Callers outside Service
// should depend on one of the narrow ports below instead of this aggregate.
type Repository interface {
	InfrastructureRepository
	IdentityRepository
	ProviderRepository
	ProjectRepository
	ConversationRepository
	TaskRepository
	RunRepository
	ResourceRepository
	ApprovalReceiptRepository
	ScheduleRepository
	ConversationQueueRepository
	RuntimeRepository
}

type InfrastructureRepository interface {
	Ping(ctx context.Context) error
	Bootstrap(ctx context.Context, actor core.Actor, organization core.Organization) error
}

type IdentityRepository interface {
	GetActor(ctx context.Context, scope core.Scope) (core.Actor, error)
	CreateAuthSession(ctx context.Context, organizationID string, input core.CreateAuthSessionInput) (core.AuthSession, error)
	ConfigureLocalPassword(ctx context.Context, organizationID, userID, password string) error
	RegisterLocalUser(ctx context.Context, organizationID string, input core.LocalRegisterInput) (core.AuthSession, error)
	AuthenticateLocal(ctx context.Context, organizationID string, input core.LocalLoginInput) (core.AuthSession, error)
	ResolveAuthSession(ctx context.Context, token string) (core.AuthSession, error)
	RevokeAuthSession(ctx context.Context, sessionID, organizationID string) error
}

type ProviderRepository interface {
	GetModelProviderConfig(ctx context.Context, organizationID string) (core.ModelProviderConfig, error)
	UpsertModelProviderConfig(ctx context.Context, organizationID string, input core.UpsertModelProviderConfigInput) (core.ModelProviderConfig, error)
	DeleteModelProviderConfig(ctx context.Context, organizationID string) error
	ListProviderInstances(ctx context.Context, organizationID string) ([]core.ProviderInstance, error)
	GetProviderInstance(ctx context.Context, organizationID, providerID string) (core.ProviderInstance, error)
	GetDefaultProviderInstance(ctx context.Context, organizationID string) (core.ProviderInstance, error)
	UpsertProviderInstance(ctx context.Context, organizationID string, input core.UpsertProviderInstanceInput) (core.ProviderInstance, error)
	DeleteProviderInstance(ctx context.Context, organizationID, providerID string) error
	ReplaceModelCatalog(ctx context.Context, organizationID, providerID string, entries []core.ModelCatalogEntry) error
	ListModelCatalogEntries(ctx context.Context, organizationID, providerID string) ([]core.ModelCatalogEntry, error)
}

type ProjectRepository interface {
	CreateProject(ctx context.Context, scope core.Scope, input core.CreateProjectInput, idempotency core.Idempotency) (core.Project, error)
	UpdateProjectSettings(ctx context.Context, scope core.Scope, projectID string, input core.UpdateProjectSettingsInput, idempotency core.Idempotency) (core.Project, error)
	GetProject(ctx context.Context, scope core.Scope, projectID string) (core.Project, error)
	ListProjects(ctx context.Context, scope core.Scope, limit int) ([]core.Project, error)
	AddProjectMember(ctx context.Context, scope core.Scope, projectID string, input core.AddProjectMemberInput, correlationID string, idempotency core.Idempotency) (core.ProjectMember, error)
	ListProjectMembers(ctx context.Context, scope core.Scope, projectID string) ([]core.ProjectMember, error)
	RemoveProjectMember(ctx context.Context, scope core.Scope, projectID, userID, correlationID string, idempotency core.Idempotency) error
	CreateProjectInvitation(ctx context.Context, scope core.Scope, projectID string, input core.CreateProjectInvitationInput, correlationID string, idempotency core.Idempotency) (core.ProjectInvitation, error)
	ListProjectInvitations(ctx context.Context, scope core.Scope, projectID string) ([]core.ProjectInvitation, error)
	AcceptProjectInvitation(ctx context.Context, token, correlationID string, idempotency core.Idempotency) (core.ProjectInvitation, error)
	RevokeProjectInvitation(ctx context.Context, scope core.Scope, invitationID, correlationID string, idempotency core.Idempotency) error
	CreatePlanItem(ctx context.Context, scope core.Scope, projectID string, input core.CreatePlanItemInput, idempotency core.Idempotency) (core.PlanItem, error)
	ListPlanItems(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.PlanItem, error)
	MovePlanItem(ctx context.Context, scope core.Scope, planItemID string, input core.MovePlanItemInput, idempotency core.Idempotency) (core.PlanItem, error)
	ListProjectActivity(ctx context.Context, scope core.Scope, projectID, before string, limit int) ([]core.ProjectActivity, error)
}

type ConversationRepository interface {
	GetConversation(ctx context.Context, scope core.Scope, conversationID string) (core.Conversation, error)
	RenameConversation(ctx context.Context, scope core.Scope, conversationID, title string) (core.Conversation, error)
	DeleteConversation(ctx context.Context, scope core.Scope, conversationID, correlationID string) error
	ListConversations(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.Conversation, error)
	ListMessages(ctx context.Context, scope core.Scope, conversationID string, after int64, limit int) ([]core.Message, error)
	ListSessionEntries(ctx context.Context, scope core.Scope, conversationID string, afterEntrySeq int64, limit int) (core.SessionTreePage, error)
	GetSessionEntryPath(ctx context.Context, scope core.Scope, entryID string) ([]core.SessionEntry, error)
	ListSessionPathMessages(ctx context.Context, scope core.Scope, conversationID, leafEntryID string, afterServerSeq int64, limit int) ([]core.Message, error)
	ImportTranscript(ctx context.Context, scope core.Scope, projectID string, input core.ImportTranscriptInput, idempotency core.Idempotency) (core.ImportTranscriptResult, error)
	NavigateSession(ctx context.Context, scope core.Scope, conversationID string, input core.NavigateSessionInput) (core.NavigateSessionResult, error)
	ForkSession(ctx context.Context, scope core.Scope, conversationID string, input core.ForkSessionInput) (core.Conversation, error)
	LabelSessionEntry(ctx context.Context, scope core.Scope, conversationID, entryID string, input core.LabelSessionEntryInput) (core.SessionEntry, error)
	AppendBranchSummary(ctx context.Context, scope core.Scope, conversationID string, input core.BranchSummaryInput) (core.SessionEntry, error)
	GetConversationContextSettings(ctx context.Context, scope core.Scope) (core.ConversationContextSettings, error)
	UpsertConversationContextSettings(ctx context.Context, scope core.Scope, input core.UpdateConversationContextSettingsInput) (core.ConversationContextSettings, error)
	GetLatestConversationCompaction(ctx context.Context, scope core.Scope, conversationID string) (core.ConversationCompaction, error)
	SaveConversationCompaction(ctx context.Context, scope core.Scope, compaction core.ConversationCompaction) (core.ConversationCompaction, error)
	AppendChatTurn(ctx context.Context, scope core.Scope, projectID string, input core.ProjectTurnInput, assistant string, calls []core.ConversationModelCall, idempotency core.Idempotency) (core.ProjectTurnResult, error)
	RecordConversationModelCalls(ctx context.Context, scope core.Scope, projectID, conversationID string, calls []core.ConversationModelCall) error
}

type TaskRepository interface {
	CreateTask(ctx context.Context, scope core.Scope, projectID string, input core.CreateTaskInput, configSnapshot map[string]any, correlationID string, idempotency core.Idempotency) (core.Task, error)
	ControlTask(ctx context.Context, scope core.Scope, taskID string, input core.ControlTaskInput, correlationID string, idempotency core.Idempotency) (core.Task, error)
	AcceptTaskOutcome(ctx context.Context, scope core.Scope, taskID, note, correlationID string, idempotency core.Idempotency) (core.AcceptedOutcome, error)
	GetTask(ctx context.Context, scope core.Scope, taskID string) (core.Task, error)
	ListTasks(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.Task, error)
	HasActiveTask(ctx context.Context, scope core.Scope, conversationID string) (bool, string, error)
}

type RunRepository interface {
	ListRunEvents(ctx context.Context, scope core.Scope, runID string, after int64, limit int) ([]core.RunEvent, error)
	GetActiveRunPlan(ctx context.Context, scope core.Scope, runID string) (core.RunPlanRevision, error)
	GetRunUsage(ctx context.Context, scope core.Scope, runID string) (core.RunUsage, error)
	GetProjectUsage(ctx context.Context, scope core.Scope, projectID string, since time.Time) (core.ProjectUsage, error)
	UpdateRunBudget(ctx context.Context, scope core.Scope, runID string, input core.UpdateRunBudgetInput, correlationID string) error
	CreateAdvisorReport(ctx context.Context, scope core.Scope, runID string, input core.CreateAdvisorInput, correlationID string) (core.AdvisorReport, error)
	ListRunArtifacts(ctx context.Context, scope core.Scope, runID string) ([]core.Artifact, error)
	GetAgentTeamRoom(ctx context.Context, scope core.Scope, runID string) (core.AgentTeamRoom, error)
}

type ResourceRepository interface {
	CreateAssetFolder(ctx context.Context, scope core.Scope, projectID string, input core.CreateAssetFolderInput, idempotency core.Idempotency) (core.Asset, error)
	GetAsset(ctx context.Context, scope core.Scope, assetID string) (core.Asset, error)
	UploadAsset(ctx context.Context, scope core.Scope, projectID string, input core.UploadAssetInput, content io.Reader, maxFileBytes, quotaBytes int64) (core.Asset, error)
	ListAssets(ctx context.Context, scope core.Scope, projectID, parentID, query, kind string, limit int, quotaBytes int64) (core.AssetCollection, error)
	OpenAssetContent(ctx context.Context, scope core.Scope, assetID string) (core.Asset, io.ReadCloser, error)
	OpenArtifactContent(ctx context.Context, scope core.Scope, artifactID string) (core.Artifact, io.ReadCloser, error)
	PublishArtifact(ctx context.Context, scope core.Scope, artifactID string, input core.PublishArtifactInput, quotaBytes int64, idempotency core.Idempotency) (core.Asset, error)
	CreateCapability(ctx context.Context, scope core.Scope, projectID string, input core.CreateCapabilityInput, contentHash, status, riskLevel string, idempotency core.Idempotency) (core.Capability, error)
	GetCapability(ctx context.Context, scope core.Scope, capabilityID string) (core.Capability, error)
	ListCapabilities(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.Capability, error)
	SetCapabilityStatus(ctx context.Context, scope core.Scope, capabilityID, status string, idempotency core.Idempotency) (core.Capability, error)
	DeleteCapability(ctx context.Context, scope core.Scope, capabilityID, correlationID string) error
	ReviewCapability(ctx context.Context, scope core.Scope, capabilityID string, input core.ReviewCapabilityInput, correlationID string, idempotency core.Idempotency) (core.Capability, error)
	ConnectMCP(ctx context.Context, scope core.Scope, capabilityID, credential, correlationID string) (core.MCPHealth, error)
	DisconnectMCP(ctx context.Context, scope core.Scope, capabilityID, correlationID string) (core.MCPHealth, error)
	GetMCPHealth(ctx context.Context, scope core.Scope, capabilityID string) (core.MCPHealth, error)
	CreateMemory(ctx context.Context, scope core.Scope, projectID string, input core.CreateMemoryInput, sourceHash string, idempotency core.Idempotency) (core.Memory, error)
	ListMemories(ctx context.Context, scope core.Scope, projectID, query string, limit int) ([]core.Memory, error)
	UpdateMemory(ctx context.Context, scope core.Scope, memoryID string, input core.UpdateMemoryInput, sourceHash string, idempotency core.Idempotency) (core.Memory, error)
	SetMemoryStatus(ctx context.Context, scope core.Scope, memoryID, status string, idempotency core.Idempotency) (core.Memory, error)
	DeleteMemory(ctx context.Context, scope core.Scope, memoryID, correlationID string, idempotency core.Idempotency) error
	PromoteMemoryToSkill(ctx context.Context, scope core.Scope, memoryID string, idempotency core.Idempotency) (core.Capability, error)
	CreateAgentDefinition(ctx context.Context, scope core.Scope, projectID string, input core.CreateAgentInput, status string, idempotency core.Idempotency) (core.AgentDefinition, error)
	ListAgentDefinitions(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.AgentDefinition, error)
	SetAgentDefinitionStatus(ctx context.Context, scope core.Scope, agentID, status string, idempotency core.Idempotency) (core.AgentDefinition, error)
	DeleteAgentDefinition(ctx context.Context, scope core.Scope, agentID, correlationID string) error
	CreateTeamDefinition(ctx context.Context, scope core.Scope, projectID string, input core.CreateTeamDefinitionInput, idempotency core.Idempotency) (core.TeamDefinition, error)
	ListTeamDefinitions(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.TeamDefinition, error)
	SetTeamDefinitionStatus(ctx context.Context, scope core.Scope, teamID, status string, idempotency core.Idempotency) (core.TeamDefinition, error)
}

type ApprovalReceiptRepository interface {
	ListApprovals(ctx context.Context, scope core.Scope, status string, limit int) ([]core.Approval, error)
	ListHumanActions(ctx context.Context, scope core.Scope, status, conversationID, runID string, limit int) ([]core.HumanAction, error)
	DecideHumanAction(ctx context.Context, scope core.Scope, actionID string, input core.DecideHumanActionInput, correlationID string, idempotency core.Idempotency) (core.HumanAction, error)
	ExpireApprovals(ctx context.Context, now time.Time) (int, error)
	ListInboxHistory(ctx context.Context, scope core.Scope, limit int) ([]core.InboxHistoryItem, error)
	DecideApproval(ctx context.Context, scope core.Scope, approvalID string, input core.DecideApprovalInput, correlationID string, idempotency core.Idempotency) (core.Approval, error)
	ListProjectApprovalRules(ctx context.Context, scope core.Scope, projectID string) ([]core.ApprovalRule, error)
	RevokeProjectApprovalRule(ctx context.Context, scope core.Scope, projectID, ruleID, correlationID string) error
	ListSideEffectReceipts(ctx context.Context, scope core.Scope, status string, limit int) ([]core.SideEffectReceipt, error)
	ResolveSideEffect(ctx context.Context, scope core.Scope, receiptID string, input core.ResolveSideEffectInput, correlationID string, idempotency core.Idempotency) (core.SideEffectReceipt, error)
}

type ScheduleRepository interface {
	CreateSchedule(ctx context.Context, scope core.Scope, projectID string, input core.CreateScheduleInput, nextRunAt time.Time, idempotency core.Idempotency) (core.Schedule, error)
	ListSchedules(ctx context.Context, scope core.Scope, projectID string, limit int) ([]core.Schedule, error)
	SetScheduleStatus(ctx context.Context, scope core.Scope, scheduleID, status string, idempotency core.Idempotency) (core.Schedule, error)
	GetDueSchedule(ctx context.Context, now time.Time) (core.Schedule, error)
	ClaimScheduleFire(ctx context.Context, schedule core.Schedule, nextRunAt time.Time) (core.ScheduleFire, error)
	CreateScheduledTask(ctx context.Context, schedule core.Schedule, fire core.ScheduleFire, correlationID string) (core.Task, error)
	AdvanceScheduleAfterFire(ctx context.Context, scheduleID string, plannedAt, nextRunAt time.Time, fireStatus, taskID string) error
}

type ConversationQueueRepository interface {
	EnqueueConversationTurn(ctx context.Context, scope core.Scope, projectID, conversationID, activeTaskID, content, inputJSON, kind string) (core.ConversationQueueItem, error)
	ListConversationQueue(ctx context.Context, scope core.Scope, conversationID string, status string) ([]core.ConversationQueueItem, error)
	ApplyConversationSteering(ctx context.Context, scope core.Scope, sourceStep core.ClaimedStep, run core.RunContext, correlationID string) (core.ConversationQueueItem, string, error)
	ClaimSteeringAfterStep(ctx context.Context, scope core.Scope, taskID, conversationID string) (core.ConversationQueueItem, error)
	ClaimNextFollowUp(ctx context.Context, scope core.Scope, conversationID string) (core.ConversationQueueItem, error)
	ClaimNextIdleFollowUp(ctx context.Context, retryBefore time.Time) (core.ConversationQueueItem, error)
	MarkConversationQueueItemConsumed(ctx context.Context, scope core.Scope, itemID string) (core.ConversationQueueItem, error)
	CancelConversationQueueItems(ctx context.Context, scope core.Scope, conversationID, taskID string) error
	CancelConversationQueueItem(ctx context.Context, scope core.Scope, conversationID, itemID string) (core.ConversationQueueItem, error)
	PromoteConversationQueueItemToSteering(ctx context.Context, scope core.Scope, conversationID, itemID string) (core.ConversationQueueItem, error)
	RequeueConversationQueueItem(ctx context.Context, scope core.Scope, itemID string) error
}

// RuntimeRepository is the durable boundary consumed by the worker/harness.
type RuntimeRepository interface {
	ClaimNextStep(ctx context.Context, workerID string, now, leaseUntil time.Time) (core.ClaimedStep, error)
	HeartbeatStep(ctx context.Context, step core.ClaimedStep, now, leaseUntil time.Time) (bool, error)
	GetRunContext(ctx context.Context, runID string) (core.RunContext, error)
	MarkStepStarted(ctx context.Context, step core.ClaimedStep, run core.RunContext, correlationID string) error
	CommitCheckpoint(ctx context.Context, step core.ClaimedStep, run core.RunContext, kind string, state map[string]any, correlationID string) error
	BeginToolExecution(ctx context.Context, step core.ClaimedStep, run core.RunContext, call core.ToolCall, inputHash string, idempotent bool, correlationID string) (string, error)
	FinishStep(ctx context.Context, step core.ClaimedStep, run core.RunContext, outcome core.StepOutcome, correlationID string) error
}
