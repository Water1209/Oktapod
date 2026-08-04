package core

import (
	"errors"
	"time"
)

const (
	ProfilePersonal = "personal"
	ProfileTeam     = "team"

	ProjectKindPersonal = "personal"
	ProjectKindTeam     = "team"

	SceneWork = "work"
	SceneCode = "code"

	ModeAnswer      = "answer"
	ModeSingleAgent = "single_agent"
	ModeAgentTeam   = "agent_team"
	ModeAuto        = "auto"

	// PermissionModeDefault is accepted only for backward compatibility and
	// normalized to PermissionModeAsk before a Run snapshot is persisted.
	PermissionModeDefault    = "default"
	PermissionModeAsk        = "ask"
	PermissionModeAutoReview = "auto_review"
	PermissionModeFullAccess = "full_access"

	VisibilityPrivate  = "private"
	VisibilitySelected = "selected"
	VisibilityProject  = "project"

	StatusQueued          = "queued"
	StatusRunning         = "running"
	StatusWaitingApproval = "waiting_approval"
	StatusWaitingInput    = "waiting_input"
	StatusPaused          = "paused"
	StatusBlocked         = "blocked"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusCanceled        = "canceled"

	TaskCommandPause  = "pause"
	TaskCommandResume = "resume"
	TaskCommandCancel = "cancel"
	TaskCommandRetry  = "retry"

	ToolWorkspaceWriteFile      = "workspace_write_file"
	ToolWorkspaceReadFile       = "workspace_read_file"
	ToolWorkspaceListFiles      = "workspace_list_files"
	ToolWorkspaceFindFiles      = "workspace_find_files"
	ToolWorkspaceGrep           = "workspace_grep"
	ToolWorkspaceEditFile       = "workspace_edit_file"
	ToolWorkspaceRunCommand     = "workspace_run_command"
	ToolWorkspaceShell          = "workspace_shell"
	ToolGitStatus               = "git_status"
	ToolGitDiff                 = "git_diff"
	ToolGitLog                  = "git_log"
	ToolGitBranch               = "git_branch"
	ToolGitCommit               = "git_commit"
	ToolGitCommitPaths          = "git_commit_paths"
	ToolGitRestore              = "git_restore"
	ToolGitRestoreHunk          = "git_restore_hunk"
	ToolGitConflicts            = "git_conflicts"
	ToolWebSearch               = "web_search"
	ToolSkillInvoke             = "skill_invoke"
	ToolUpdatePlan              = "update_plan"
	ToolRequestInput            = "request_input"
	ToolRequestPermission       = "request_permission"
	ToolWorkspaceExtractArchive = "workspace_extract_archive"

	PolicyAllow = "allow"
	PolicyAsk   = "ask"
	PolicyDeny  = "deny"

	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"

	ApprovalScopeOnce         = "once"
	ApprovalScopeConversation = "conversation"
	ApprovalScopeProject      = "project"

	HumanActionToolApproval         = "tool_approval"
	HumanActionPermissionEscalation = "permission_escalation"
	HumanActionInputRequest         = "input_request"
	HumanActionBudgetRequest        = "budget_request"
	HumanActionSideEffectResolution = "side_effect_resolution"

	HumanActionReviewing   = "reviewing"
	HumanActionWaitingUser = "waiting_user"
	HumanActionApproved    = "approved"
	HumanActionRejected    = "rejected"
	HumanActionAnswered    = "answered"
	HumanActionExpired     = "expired"
	HumanActionSuperseded  = "superseded"

	HumanActionReviewerUser = "user"
	HumanActionReviewerAuto = "auto"

	RunPlanActive     = "active"
	RunPlanSuperseded = "superseded"
	RunPlanCompleted  = "completed"
	RunPlanBlocked    = "blocked"
	RunPlanCanceled   = "canceled"

	RunPlanItemPending     = "pending"
	RunPlanItemRunning     = "running"
	RunPlanItemWaitingUser = "waiting_user"
	RunPlanItemCompleted   = "completed"
	RunPlanItemBlocked     = "blocked"
	RunPlanItemCanceled    = "canceled"

	// DefaultApprovalTTL keeps unattended high-risk actions from remaining
	// indefinitely actionable. Expiry is a deterministic rejection, never an
	// automatic execution.
	DefaultApprovalTTL = 24 * time.Hour

	// Task runtime defaults follow mature coding-agent budgets: bounded UI
	// quotas, three recoverable retries, and conservative output ramps. API
	// callers may still explicitly choose unlimited values where supported.
	DefaultTaskMaxModelCalls = 32
	DefaultTaskMaxTokens     = 200_000
	DefaultModelRetryLimit   = 3
	DefaultToolRetryLimit    = 5
	DefaultModelOutputTokens = 2_048

	SideEffectReceiptStarted   = "started"
	SideEffectReceiptSucceeded = "succeeded"
	SideEffectReceiptFailed    = "failed"
	SideEffectReceiptUnknown   = "unknown"

	SideEffectDecisionConfirmApplied    = "confirm_applied"
	SideEffectDecisionConfirmNotApplied = "confirm_not_applied"
	SideEffectDecisionAbandon           = "abandon"

	PlanStatusTodo       = "todo"
	PlanStatusInProgress = "in_progress"
	PlanStatusPaused     = "paused"
	PlanStatusDone       = "done"

	PlanSourceManual = "manual"
	PlanSourceTask   = "task"

	AssetKindFile   = "file"
	AssetKindFolder = "folder"

	AssetSourceUpload   = "upload"
	AssetSourceArtifact = "artifact"

	ProjectRoleOwner    = "owner"
	ProjectRoleMember   = "member"
	ProjectRoleApprover = "approver"

	InvitationStatusPending  = "pending"
	InvitationStatusAccepted = "accepted"
	InvitationStatusRevoked  = "revoked"
	InvitationStatusExpired  = "expired"

	ScheduleStatusEnabled  = "enabled"
	ScheduleStatusDisabled = "disabled"
	ScheduleFirePending    = "pending"
	ScheduleFireCreated    = "created"
	ScheduleFireSkipped    = "skipped"
	ScheduleFireWaiting    = "waiting_confirmation"
	ScheduleMisfireSkip    = "skip"
	ScheduleMisfireRunNow  = "run_now"
	ScheduleMisfireAsk     = "ask"
	ScheduleOverlapForbid  = "forbid"
	ScheduleOverlapQueue   = "queue"
	ScheduleOverlapReplace = "replace"

	CapabilityKindSkill         = "skill"
	CapabilityKindPlugin        = "plugin"
	CapabilityKindMCP           = "mcp"
	CapabilityKindAgent         = "agent"
	CapabilityStatusEnabled     = "enabled"
	CapabilityStatusDisabled    = "disabled"
	CapabilityStatusQuarantined = "quarantined"
	CapabilityReviewApproved    = "approved"
	CapabilityReviewRejected    = "rejected"
	CapabilityScopeGlobal       = "global"
	CapabilityScopeProject      = "project"

	StepAgentCoordinatorPlan      = "agent_coordinator_plan"
	StepAgentWorker               = "agent_worker"
	StepAgentCoordinatorSynthesis = "agent_coordinator_synthesis"
	StepModelSynthesis            = "model_synthesis"
	StepModelSkillResume          = "model_skill_resume"
	StepModelContinue             = "model_continue"
	StepHumanActionReview         = "human_action_review"
)

const TrustExternalUntrusted = "external_untrusted"

type ExternalContentEnvelope struct {
	Trust      string
	Source     string
	Capability string
}

var (
	ErrNoWork            = errors.New("no ready work")
	ErrInvalidCredential = errors.New("invalid credentials")
	ErrUsernameTaken     = errors.New("username already exists")
)

type Scope struct {
	OrganizationID string
	UserID         string
}

type AuthSession struct {
	ID             string
	OrganizationID string
	UserID         string
	DisplayName    string
	Token          string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	LastSeenAt     time.Time
}

type CreateAuthSessionInput struct {
	UserID           string
	ExpiresInSeconds int64
}

type LocalLoginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LocalRegisterInput struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

type Actor struct {
	ID          string
	Username    string
	DisplayName string
}

type Organization struct {
	ID   string
	Name string
}

type Bootstrap struct {
	Profile         string
	ModelEndpoint   string
	ModelConfigured bool
	Actor           Actor
	Organization    Organization
	Capabilities    RuntimeCapabilities
}

// RuntimeCapabilities is an explicit, user-visible statement of the
// enforcement available in the current process. Labels describe actual
// guarantees; they must not imply container or OS isolation when running in
// the native Personal/Team adapter.
type RuntimeCapabilities struct {
	OS         string
	Sandbox    string
	Filesystem string
	Command    string
	Network    string
	Browser    string
	MCP        string
}

type Project struct {
	ID             string
	OrganizationID string
	OwnerUserID    string
	Name           string
	Description    string
	Kind           string
	Scene          string
	DefaultModel   string
	FallbackModel  string
	MaxModelCalls  int64
	MaxTokens      int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateProjectInput struct {
	Name          string
	Description   string
	Kind          string
	Scene         string
	DefaultModel  string
	FallbackModel string
}

type UpdateProjectSettingsInput struct {
	DefaultModel  *string
	FallbackModel *string
	MaxModelCalls *int64
	MaxTokens     *int64
}

type ProjectMember struct {
	UserID      string
	DisplayName string
	Email       string
	Role        string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type AddProjectMemberInput struct {
	Email       string
	DisplayName string
	Role        string
}

type ProjectInvitation struct {
	ID             string
	OrganizationID string
	ProjectID      string
	InvitedBy      string
	Email          string
	DisplayName    string
	Role           string
	Status         string
	AcceptedUserID string
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	InviteToken    string
	// SessionToken is transient: it is returned only once when a one-time
	// invitation is accepted and is never persisted or included in listings.
	SessionToken string
}

type CreateProjectInvitationInput struct {
	Email       string
	DisplayName string
	Role        string
	ExpiresIn   int64
}

type Idempotency struct {
	Key         string
	RequestHash string
}

type Task struct {
	ID                string
	OrganizationID    string
	ProjectID         string
	OwnerUserID       string
	RunID             string
	ConversationID    string
	Title             string
	Goal              string
	Acceptance        string
	Mode              string
	Visibility        string
	Status            string
	ErrorCode         string
	ResultSummary     string
	AcceptedOutcomeID string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type Conversation struct {
	ID             string
	OrganizationID string
	ProjectID      string
	OwnerUserID    string
	Title          string
	Visibility     string
	MessageCount   int64
	LastMessage    string
	// Session Tree：当前活动 Leaf Entry；空表示尚未生成 Entry（旧数据兼容）。
	ActiveLeafEntryID string
	// Fork 来源 Session；空表示非 Fork 创建的根 Session。
	ParentSessionID   string
	ForkedFromEntryID string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Session Entry 类型。树结构承载在 session_entries；messages 仍是消息正文
// 的唯一存储，Entry 通过 ref_id 引用。
const (
	SessionEntryMessage       = "message"
	SessionEntryConfigChange  = "config_change"
	SessionEntryCompaction    = "compaction"
	SessionEntryCustom        = "custom"
	SessionEntryBranchSummary = "branch_summary"
)

// SessionEntry 是 Session Tree 的一个节点。父子关系构成分支结构；同一
// Conversation 下 seq 单调递增并与 messages.server_seq 对齐。
type SessionEntry struct {
	ID               string
	OrganizationID   string
	ProjectID        string
	ConversationID   string
	ParentEntryID    string
	Seq              int64
	EntrySeq         int64
	Kind             string
	RefID            string
	Label            string
	Summary          string
	Namespace        string
	PayloadJSON      string
	SchemaVersion    int
	ContextInjection string
	CreatedAt        time.Time
}

type Message struct {
	ID             string
	OrganizationID string
	ProjectID      string
	ConversationID string
	OwnerUserID    string
	TaskID         string
	ServerSeq      int64
	Role           string
	Visibility     string
	Content        string
	CreatedAt      time.Time
}

// ConversationContextSettings controls when a user's conversation history is
// summarized for model context. Original messages remain immutable; the
// settings only affect the materialized prompt sent to the provider.
type ConversationContextSettings struct {
	OrganizationID   string
	OwnerUserID      string
	AutoCompact      bool
	TriggerTokens    int64
	RetainTokens     int64
	SummaryMaxTokens int64
	UpdatedAt        time.Time
}

type UpdateConversationContextSettingsInput struct {
	AutoCompact      bool
	TriggerTokens    int64
	RetainTokens     int64
	SummaryMaxTokens int64
}

// ConversationCompaction is a durable summary checkpoint. Messages up to
// CompactedThroughSeq are represented by Summary when the next model context
// is built; they are never deleted from the conversation or audit history.
type ConversationCompaction struct {
	ID                  string
	OrganizationID      string
	ProjectID           string
	ConversationID      string
	OwnerUserID         string
	Summary             string
	CompactedThroughSeq int64
	RetainedFromSeq     int64
	SourceMessageCount  int64
	SourceTokens        int64
	Trigger             string
	SplitMessageID      string
	SplitPrefixRunes    int64
	EntryID             string
	ExpectedLeafEntryID string
	CreatedAt           time.Time
}

// ProjectTurnInput describes one submission from the project conversation
// composer. It deliberately reuses the task execution knobs so an Auto turn
// can be promoted to a Task without asking the user to fill a second form.
type ProjectTurnInput struct {
	ConversationID   string
	TaskID           string
	Content          string
	Mode             string
	Acceptance       string
	Visibility       string
	PermissionMode   string
	SceneOverride    string
	Model            string
	FallbackModel    string
	ProviderID       string
	Thinking         string
	MaxModelCalls    int64
	MaxTokens        int64
	AgentID          string
	TeamDefinitionID string
	TeamRoles        []TeamRole
	ContextAssetIDs  []string
	ContextMemoryIDs []string
	SkillID          string
	SkillArgs        string
	QueueKind        string
}

type ConversationModelCall struct {
	Purpose        string
	ThroughEntryID string
	Model          ModelResult
}

type ConversationContextUsage struct {
	ConversationID   string
	Purpose          string
	ContextTokens    int64
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	SemanticsVersion string
	CreatedAt        time.Time
}

type SessionTreePage struct {
	Entries           []SessionEntry
	ActiveLeafEntryID string
	NextEntrySeq      int64
}

type PortableTranscriptMessage struct {
	Role      string
	Content   string
	CreatedAt time.Time
}

type PortableTranscriptConversation struct {
	Title    string
	Messages []PortableTranscriptMessage
}

type ImportTranscriptInput struct {
	SourceFormat  string
	Conversations []PortableTranscriptConversation
}

type ImportTranscriptResult struct {
	ProjectID       string
	ConversationIDs []string
	MessageCount    int
}

type NavigateSessionInput struct {
	EntryID             string
	ExpectedLeafEntryID string
}

type NavigateSessionResult struct {
	Conversation Conversation
	Entry        SessionEntry
	ComposerText string
}

type ForkSessionInput struct {
	EntryID string
	Title   string
	Clone   bool
}

type LabelSessionEntryInput struct {
	Label               string
	ExpectedLeafEntryID string
}

type BranchSummaryInput struct {
	Summary             string
	ExpectedLeafEntryID string
}

type ConversationFileContext struct {
	ReadFiles     []string
	ModifiedFiles []string
}

type ProjectTurnResult struct {
	Queued    bool
	QueueItem *ConversationQueueItem

	Route        string
	Conversation Conversation
	Messages     []Message
	Task         *Task
}

type ConversationQueueItem struct {
	ID             string
	OrganizationID string
	ProjectID      string
	ConversationID string
	ActiveTaskID   string
	Content        string
	InputJSON      string
	Kind           string
	Status         string
	CreatedAt      time.Time
	ClaimedAt      time.Time
	ConsumedAt     time.Time
}

type ProjectActivity struct {
	ID         string
	ProjectID  string
	RunID      string
	TaskID     string
	TaskTitle  string
	TaskStatus string
	Mode       string
	EventType  string
	Visibility string
	Payload    map[string]any
	OccurredAt time.Time
}

type PlanItem struct {
	ID             string
	OrganizationID string
	ProjectID      string
	OwnerUserID    string
	Title          string
	Description    string
	Status         string
	Source         string
	LinkedTaskID   string
	Visibility     string
	Rank           int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreatePlanItemInput struct {
	Title       string
	Description string
	Visibility  string
}

type MovePlanItemInput struct {
	Status string
}

type CreateTaskInput struct {
	ConversationID   string
	Title            string
	Goal             string
	Acceptance       string
	Model            string
	FallbackModel    string
	ProviderID       string
	Thinking         string
	Mode             string
	Visibility       string
	PermissionMode   string
	SceneOverride    string
	MaxModelCalls    int64
	MaxTokens        int64
	AgentID          string
	TeamDefinitionID string
	TeamRoles        []TeamRole
	ContextAssetIDs  []string
	ContextMemoryIDs []string
	SkillID          string
	SkillArgs        string
}

// TeamRole is an immutable per-Run role definition. Empty TeamRoles keeps the
// MVP default of Coordinator + Researcher + Builder + Reviewer.
type TeamRole struct {
	Role string `json:"role"`
	Name string `json:"name"`
}

type ControlTaskInput struct {
	Command string
}

type AcceptedOutcome struct {
	ID             string
	OrganizationID string
	ProjectID      string
	TaskID         string
	RunID          string
	AcceptedBy     string
	Note           string
	CreatedAt      time.Time
}

type Artifact struct {
	ID          string
	ProjectID   string
	TaskID      string
	RunID       string
	StepID      string
	Name        string
	Kind        string
	MediaType   string
	ContentHash string
	SizeBytes   int64
	Visibility  string
	SourceType  string
	CreatedAt   time.Time
}

type Asset struct {
	ID               string
	OrganizationID   string
	ProjectID        string
	OwnerUserID      string
	ParentID         string
	Name             string
	Kind             string
	MediaType        string
	ContentHash      string
	SizeBytes        int64
	Visibility       string
	SourceType       string
	SourceArtifactID string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type AssetCollection struct {
	Items        []Asset
	VisibleBytes int64
	QuotaBytes   int64
}

type CreateAssetFolderInput struct {
	Name       string
	ParentID   string
	Visibility string
}

type UploadAssetInput struct {
	Name       string
	ParentID   string
	Visibility string
	MediaType  string
	SizeHint   int64
}

type PublishArtifactInput struct {
	Name       string
	ParentID   string
	Visibility string
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
	// Sequential forces the entire source-model ToolBatch to execute in source
	// order. It is registry metadata, never a model-controlled argument.
	Sequential bool
}

type ToolExecution struct {
	ToolCallID   string
	InvocationID string
	Status       string
	ErrorCode    string
	ExitCode     int
	Output       map[string]any
	InputHash    string
	Idempotent   bool
}

type Approval struct {
	ID             string
	OrganizationID string
	ProjectID      string
	TaskID         string
	RunID          string
	StepID         string
	ToolCallID     string
	ToolName       string
	RiskLevel      string
	Status         string
	Reason         string
	FrozenParams   map[string]any
	InputHash      string
	RequestedBy    string
	DecidedBy      string
	RequestedAt    time.Time
	ExpiresAt      time.Time
	DecidedAt      *time.Time
}

type RunPlanRevision struct {
	ID                   string
	OrganizationID       string
	ProjectID            string
	TaskID               string
	RunID                string
	Revision             int64
	Trigger              string
	GoalSummary          string
	Status               string
	SupersedesRevisionID string
	CreatedBy            string
	CreatedAt            time.Time
	SupersededAt         *time.Time
	Items                []RunPlanItem
}

type RunPlanItem struct {
	ID             string
	PlanRevisionID string
	Position       int64
	Title          string
	Description    string
	Status         string
	ResultSummary  string
	StartedAt      *time.Time
	CompletedAt    *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type HumanAction struct {
	ID             string
	OrganizationID string
	ProjectID      string
	ConversationID string
	TaskID         string
	RunID          string
	StepID         string
	PlanRevisionID string
	RunPlanItemID  string
	Kind           string
	Status         string
	Reviewer       string
	RiskLevel      string
	Title          string
	Reason         string
	Request        map[string]any
	RequestHash    string
	Response       map[string]any
	RequestedBy    string
	DecidedBy      string
	RequestedAt    time.Time
	ExpiresAt      time.Time
	DecidedAt      *time.Time
	Version        int64
}

type DecideHumanActionInput struct {
	Decision  string
	Scope     string
	Answer    any
	Note      string
	Confirmed bool
	Version   int64
}

// SideEffectReceipt is the safe, scope-filtered summary of an external or
// otherwise non-idempotent action. Unknown receipts are never retried by the
// runtime; a human must explicitly resolve them.
type SideEffectReceipt struct {
	ID             string
	OrganizationID string
	ProjectID      string
	TaskID         string
	RunID          string
	StepID         string
	ToolCallID     string
	ToolName       string
	InputHash      string
	Idempotent     bool
	Status         string
	ErrorCode      string
	StartedAt      time.Time
	FinishedAt     *time.Time
}

type ResolveSideEffectInput struct {
	Decision string
	Note     string
}

// InboxItem is a scope-filtered human action that should not be hidden inside
// a chat transcript. It intentionally carries only safe summary fields; the
// linked task/approval endpoint remains the source for sensitive details.
type InboxItem struct {
	ID        string
	Kind      string
	ProjectID string
	TaskID    string
	Title     string
	Reason    string
	Status    string
	UpdatedAt time.Time
}

// InboxHistoryItem is a privacy-safe record of a previously handled human
// action. It is derived from append-only run events, so it remains available
// after the live inbox item leaves the pending queue without duplicating
// sensitive approval parameters or free-form notes.
type InboxHistoryItem struct {
	ID         string
	Kind       string
	ProjectID  string
	TaskID     string
	Title      string
	Summary    string
	Status     string
	OccurredAt time.Time
}

type DecideApprovalInput struct {
	Decision  string
	Scope     string
	Note      string
	Confirmed bool
}

type ApprovalRule struct {
	ID                    string
	OrganizationID        string
	ProjectID             string
	ToolName              string
	RuleKey               string
	CreatedFromApprovalID string
	CreatedBy             string
	CreatedAt             time.Time
	ExpiresAt             time.Time
	RevokedAt             *time.Time
	RevokedBy             string
}

type RunEvent struct {
	ID         string
	RunID      string
	StreamSeq  int64
	EventType  string
	OccurredAt time.Time
	Payload    map[string]any
}

type AgentTeamMember struct {
	ID            string
	RunID         string
	Role          string
	DisplayName   string
	Status        string
	CurrentStepID string
	Model         string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type AgentTeamMessage struct {
	ID            string
	RunID         string
	SenderAgentID string
	ServerSeq     int64
	MessageType   string
	Content       string
	ContentHash   string
	CreatedAt     time.Time
}

type AgentTeamRoom struct {
	RunID      string
	Members    []AgentTeamMember
	Messages   []AgentTeamMessage
	NextCursor int64
}

type Schedule struct {
	ID                  string
	OrganizationID      string
	ProjectID           string
	OwnerUserID         string
	Name                string
	CronExpression      string
	Timezone            string
	Goal                string
	Acceptance          string
	Mode                string
	Visibility          string
	Status              string
	MisfirePolicy       string
	OverlapPolicy       string
	MisfireGraceSeconds int64
	NextRunAt           time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type CreateScheduleInput struct {
	Name                string
	CronExpression      string
	Timezone            string
	Goal                string
	Acceptance          string
	Mode                string
	Visibility          string
	MisfirePolicy       string
	OverlapPolicy       string
	MisfireGraceSeconds int64
}

type ScheduleFire struct {
	ID         string
	ScheduleID string
	PlannedAt  time.Time
	Generation int64
	Status     string
	TaskID     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Capability struct {
	ID               string
	OrganizationID   string
	ProjectID        string
	OwnerUserID      string
	Kind             string
	Name             string
	Version          string
	ManifestJSON     string
	ContentHash      string
	Status           string
	RiskLevel        string
	PermissionDiff   []string
	ScopeType        string
	ScopeID          string
	SourceFilename   string
	BundleHash       string
	InstallJobID     string
	CredentialRef    string
	CredentialExists bool
	Availability     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

const (
	ResourceAvailabilityAvailable   = "available"
	ResourceAvailabilityDegraded    = "degraded"
	ResourceAvailabilityDisabled    = "disabled"
	ResourceAvailabilityQuarantined = "quarantined"
)

// ReviewCapabilityInput is the explicit human gate for a quarantined
// capability. A generic status update must not be able to bypass this gate.
type ReviewCapabilityInput struct {
	Decision string
	Note     string
}

const (
	MCPStatusDisabled  = "disabled"
	MCPStatusNeedsAuth = "needs-auth"
	MCPStatusConnected = "connected"
	MCPStatusFailed    = "failed"
)

type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type MCPHealth struct {
	ID            string
	CapabilityID  string
	ProjectID     string
	Transport     string
	Status        string
	Protocol      string
	ToolCount     int64
	ToolsJSON     string
	SchemaHash    string
	ConfigVersion int64
	HealthState   string
	LastError     string
	ConnectedAt   time.Time
	UpdatedAt     time.Time
}

type CreateCapabilityInput struct {
	Kind           string
	Name           string
	Version        string
	ManifestJSON   string
	ScopeType      string
	SourceFilename string
	BundleBase64   string
	BundleData     []byte
	BundleHash     string
	Credential     string
	CredentialRef  string
}

type Memory struct {
	ID             string
	OrganizationID string
	ProjectID      string
	OwnerUserID    string
	Kind           string
	Title          string
	Content        string
	SourceHash     string
	Visibility     string
	Status         string
	SourceRefsJSON string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const (
	MemoryStatusActive   = "active"
	MemoryStatusInactive = "inactive"
)

type CreateMemoryInput struct {
	Kind           string
	Title          string
	Content        string
	Visibility     string
	SourceRefsJSON string
}

type UpdateMemoryInput struct {
	Title          string
	Content        string
	Visibility     string
	SourceRefsJSON string
}

type AgentDefinition struct {
	ID             string
	OrganizationID string
	ProjectID      string
	OwnerUserID    string
	Name           string
	Version        string
	Instructions   string
	Model          string
	FallbackModel  string
	ConfigJSON     string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateAgentInput struct {
	Name          string
	Version       string
	Instructions  string
	Model         string
	FallbackModel string
	ConfigJSON    string
}

type TeamDefinition struct {
	ID             string
	OrganizationID string
	ProjectID      string
	OwnerUserID    string
	Name           string
	Version        string
	Status         string
	Roles          []TeamRole
	ConfigJSON     string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type CreateTeamDefinitionInput struct {
	Name       string
	Version    string
	Roles      []TeamRole
	ConfigJSON string
}

type ClaimedStep struct {
	ID             string
	AttemptID      string
	AttemptNumber  int64
	Recovery       bool
	OrganizationID string
	ProjectID      string
	RunID          string
	StepType       string
	AgentID        string
	InputJSON      string
	OwnerID        string
	FenceToken     int64
}

type RunContext struct {
	RunID               string
	TaskID              string
	ProjectID           string
	OrganizationID      string
	OwnerUserID         string
	ConversationID      string
	Title               string
	Goal                string
	Acceptance          string
	Mode                string
	Visibility          string
	ConfigSnapshotJSON  string
	RequestedModel      ModelRef
	EffectiveModel      ModelRef
	Thinking            string
	ModelFallbackReason string
	ModelCatalogVersion string
}

type ModelUsage struct {
	InputTokens           int64
	OutputTokens          int64
	CachedTokens          int64 // compatibility alias for CacheReadTokens
	CacheReadTokens       int64
	CacheWriteTokens      int64
	ContextTokens         int64
	ReasoningTokens       int64
	UsageSemanticsVersion string
	Estimated             bool
}

type RunUsage struct {
	RunID           string
	ModelCalls      int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	EstimatedCalls  int64
	LatencyMS       int64
	CostStatus      string
	Currency        string
	CostDecimal     string
	BudgetMaxCalls  int64
	BudgetMaxTokens int64
	BudgetStatus    string
	RemainingCalls  int64
	RemainingTokens int64
	ReservedCalls   int64
	ReservedTokens  int64
}

type RunChanges struct {
	RunID          string
	ProjectID      string
	Baseline       string
	BaselineHead   string
	CurrentHead    string
	DirtyAtStart   bool
	StatusHash     string
	Diff           string
	Truncated      bool
	BaselineNotice string
}

// ProjectUsage is an aggregate that is safe to expose only to project
// owners/approvers. Raw task titles and prompts are deliberately omitted;
// dimensions are stable identifiers/labels suitable for charts and billing
// review without leaking private task content.
type ProjectUsage struct {
	ProjectID       string
	ModelCalls      int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	EstimatedCalls  int64
	LatencyMS       int64
	CostStatus      string
	Currency        string
	CostDecimal     string
	ByDay           []UsageBucket
	ByModel         []UsageBucket
	ByMember        []UsageBucket
	ByAgent         []UsageBucket
}

type UsageBucket struct {
	Key             string
	Label           string
	ModelCalls      int64
	InputTokens     int64
	OutputTokens    int64
	CachedTokens    int64
	ReasoningTokens int64
	EstimatedCalls  int64
	LatencyMS       int64
	CostDecimal     string
}

type UpdateRunBudgetInput struct {
	MaxModelCalls int64
	MaxTokens     int64
	Approve       bool
}

type AdvisorReport struct {
	ID                   string
	OrganizationID       string
	ProjectID            string
	TaskID               string
	RunID                string
	RequestedBy          string
	TriggerReason        string
	Status               string
	Summary              string
	Evidence             []string
	EstimatedModelCalls  int64
	EstimatedTokens      int64
	EstimatedCostDecimal string
	CreatedAt            time.Time
}

type CreateAdvisorInput struct {
	TriggerReason string
	Approve       bool
}

type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
)

type ContentBlockType string

const (
	ContentBlockText       ContentBlockType = "text"
	ContentBlockToolCall   ContentBlockType = "tool_call"
	ContentBlockToolResult ContentBlockType = "tool_result"
	ContentBlockImage      ContentBlockType = "image"
	ContentBlockThinking   ContentBlockType = "thinking"
)

type ToolCallBlock struct {
	ID        string
	Name      string
	Arguments map[string]any
}

type ToolResultBlock struct {
	ToolCallID string
	ToolName   string
	Content    string
	IsError    bool
}

type ImageBlock struct {
	URL       string
	Data      string
	MediaType string
	Alt       string
}

type ThinkingBlock struct {
	Text      string
	Signature string
}

type ContentBlock struct {
	Type       ContentBlockType
	Text       string
	ToolCall   *ToolCallBlock
	ToolResult *ToolResultBlock
	Image      *ImageBlock
	Thinking   *ThinkingBlock
}

type ModelMessage struct {
	Role       MessageRole
	Content    []ContentBlock
	ToolCallID string
	ToolName   string
}

type ModelRef struct {
	ProviderID string
	ModelID    string
}

type StopReason string

const (
	StopReasonStop    StopReason = "stop"
	StopReasonToolUse StopReason = "tool_use"
	StopReasonLength  StopReason = "length"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)

type ModelResult struct {
	CallID              string
	Provider            string
	Endpoint            string
	Model               string
	RequestedModel      ModelRef
	EffectiveModel      ModelRef
	RoutingReason       string
	ProviderRequestID   string
	Content             string
	ContentBlocks       []ContentBlock
	StopReason          StopReason
	Usage               ModelUsage
	Latency             time.Duration
	ErrorCode           string
	ToolCalls           []ToolCall
	ToolArgumentRepairs int
}

// ModelDescriptor is a provider-advertised model that can be selected for an
// Agent or used as the project default. Only the stable provider id crosses
// the API boundary in MVP; pricing and capability metadata remain provider
// owned until a catalog contract is introduced.
type ModelDescriptor struct {
	ID               string
	ToolCalling      bool
	ToolCallingKnown bool
	Vision           bool
	StructuredOutput bool
	Reasoning        bool
	ContextWindow    int64
}

type StepOutcome struct {
	Status        string
	ResultSummary string
	ErrorCode     string
	Output        map[string]any
	Model         *ModelResult
	ToolExecution *ToolExecution
}
