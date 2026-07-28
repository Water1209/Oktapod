package core

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"sort"
	"strings"
)

func ValidateCreateCapability(input CreateCapabilityInput) error {
	if strings.TrimSpace(input.Name) == "" {
		return &ValidationError{Field: "name", Message: "is required"}
	}
	if len(input.Name) > 120 || len(input.Version) > 64 || len(input.ManifestJSON) > 256*1024 {
		return &ValidationError{Field: "manifest", Message: "capability metadata exceeds size limit"}
	}
	if input.ScopeType != CapabilityScopeGlobal && input.ScopeType != CapabilityScopeProject {
		return &ValidationError{Field: "scope_type", Message: "must be global or project"}
	}
	if len(input.SourceFilename) > 255 || strings.ContainsRune(input.SourceFilename, 0) {
		return &ValidationError{Field: "source_filename", Message: "must be a bounded file name"}
	}
	switch input.Kind {
	case CapabilityKindSkill, CapabilityKindPlugin, CapabilityKindMCP, CapabilityKindAgent:
	default:
		return &ValidationError{Field: "kind", Message: "must be skill, plugin, mcp, or agent"}
	}
	var manifest map[string]any
	if err := json.Unmarshal([]byte(input.ManifestJSON), &manifest); err != nil {
		return &ValidationError{Field: "manifest_json", Message: "must be valid JSON"}
	}
	if input.Kind == CapabilityKindSkill {
		if err := validateSkillManifest(manifest); err != nil {
			return err
		}
	}
	// Capabilities are declarative in MVP. Executable hooks, shell commands and
	// unrestricted network endpoints are quarantined before they can be used.
	for _, key := range []string{"command", "commands", "exec", "executable", "hooks", "network"} {
		if _, exists := manifest[key]; exists && input.Kind != CapabilityKindMCP {
			return &ValidationError{Field: "manifest_json", Message: fmt.Sprintf("field %q requires security review", key)}
		}
	}
	if input.Kind == CapabilityKindMCP {
		transport, _ := manifest["transport"].(string)
		urlValue, _ := manifest["url"].(string)
		command, _ := manifest["command"].(string)
		if transport != "stdio" && transport != "streamable_http" && transport != "http" && transport != "" {
			return &ValidationError{Field: "manifest_json.transport", Message: "must be stdio or streamable_http"}
		}
		if strings.TrimSpace(urlValue) == "" && strings.TrimSpace(command) == "" {
			return &ValidationError{Field: "manifest_json", Message: "MCP requires url or command"}
		}
		if args, exists := manifest["args"]; exists {
			if _, ok := args.([]any); !ok {
				return &ValidationError{Field: "manifest_json.args", Message: "must be an array"}
			}
		}
	}
	return nil
}

func validateSkillManifest(manifest map[string]any) error {
	description, _ := manifest["description"].(string)
	instructions, _ := manifest["instructions"].(string)
	if strings.TrimSpace(description) == "" && strings.TrimSpace(instructions) == "" {
		return &ValidationError{Field: "manifest_json", Message: "Skill requires description or instructions"}
	}
	if len([]rune(description)) > 1000 {
		return &ValidationError{Field: "manifest_json.description", Message: "must not exceed 1000 characters"}
	}
	if len([]rune(instructions)) > 64*1024 {
		return &ValidationError{Field: "manifest_json.instructions", Message: "must not exceed 65536 characters"}
	}
	if value, exists := manifest["model_invocable"]; exists {
		if _, ok := value.(bool); !ok {
			return &ValidationError{Field: "manifest_json.model_invocable", Message: "must be a boolean"}
		}
	}
	if err := validateSkillStringList(manifest, "triggers", 32, 120, false); err != nil {
		return err
	}
	if err := validateSkillStringList(manifest, "keywords", 64, 120, false); err != nil {
		return err
	}
	if err := validateSkillStringList(manifest, "allowed_tools", 64, 200, true); err != nil {
		return err
	}
	return nil
}

func validateSkillStringList(manifest map[string]any, key string, maxItems, maxLength int, toolNames bool) error {
	value, exists := manifest[key]
	if !exists {
		return nil
	}
	items, ok := value.([]any)
	if !ok || len(items) > maxItems {
		return &ValidationError{Field: "manifest_json." + key, Message: fmt.Sprintf("must be an array with at most %d strings", maxItems)}
	}
	for _, item := range items {
		text, ok := item.(string)
		text = strings.TrimSpace(text)
		if !ok || text == "" || len([]rune(text)) > maxLength {
			return &ValidationError{Field: "manifest_json." + key, Message: fmt.Sprintf("items must be non-empty strings up to %d characters", maxLength)}
		}
		if toolNames && strings.ContainsAny(text, " \t\r\n/\\:@?#") {
			return &ValidationError{Field: "manifest_json." + key, Message: "tool names may contain only portable identifier characters"}
		}
	}
	return nil
}

func ValidateReviewCapability(input ReviewCapabilityInput) error {
	switch strings.ToLower(strings.TrimSpace(input.Decision)) {
	case CapabilityReviewApproved, CapabilityReviewRejected:
	default:
		return &ValidationError{Field: "decision", Message: "must be approved or rejected"}
	}
	if len([]rune(input.Note)) > 2000 {
		return &ValidationError{Field: "note", Message: "must not exceed 2000 characters"}
	}
	return nil
}

// CapabilityPermissionDiff extracts a bounded, value-redacted permission
// summary for human review. It intentionally returns names and requested
// fields, never command arguments, URLs, tokens, or environment values.
func CapabilityPermissionDiff(manifestJSON string) []string {
	var manifest map[string]any
	if json.Unmarshal([]byte(manifestJSON), &manifest) != nil {
		return nil
	}
	items := make([]string, 0, 16)
	for _, key := range []string{"permissions", "scopes"} {
		if values, ok := manifest[key].([]any); ok {
			for _, value := range values {
				if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
					items = append(items, key+":"+strings.TrimSpace(text))
				}
			}
		}
	}
	if tools, ok := manifest["tools"].([]any); ok {
		for _, value := range tools {
			if item, ok := value.(map[string]any); ok {
				if name, ok := item["name"].(string); ok && strings.TrimSpace(name) != "" {
					items = append(items, "tool:"+strings.TrimSpace(name))
				}
			} else if name, ok := value.(string); ok && strings.TrimSpace(name) != "" {
				items = append(items, "tool:"+strings.TrimSpace(name))
			}
		}
	}
	if tools, ok := manifest["allowed_tools"].([]any); ok {
		for _, value := range tools {
			if name, ok := value.(string); ok && strings.TrimSpace(name) != "" {
				items = append(items, "allowed_tool:"+strings.TrimSpace(name))
			}
		}
	}
	for _, key := range []string{"command", "commands", "exec", "executable", "hooks", "network", "env", "secrets"} {
		if _, exists := manifest[key]; exists {
			items = append(items, "field:"+key)
		}
	}
	sort.Strings(items)
	if len(items) > 32 {
		items = items[:32]
	}
	return items
}

func ValidateCreateMemory(input CreateMemoryInput) error {
	if strings.TrimSpace(input.Title) == "" {
		return &ValidationError{Field: "title", Message: "is required"}
	}
	if strings.TrimSpace(input.Content) == "" {
		return &ValidationError{Field: "content", Message: "is required"}
	}
	if len(input.Title) > 200 || len(input.Content) > 256*1024 {
		return &ValidationError{Field: "content", Message: "memory exceeds size limit"}
	}
	if input.Kind == "" {
		return &ValidationError{Field: "kind", Message: "is required"}
	}
	if err := ValidateAssetVisibility(input.Visibility); err != nil {
		return err
	}
	if err := ValidateMemorySourceRefs(input.SourceRefsJSON); err != nil {
		return err
	}
	return nil
}

func ValidateMemorySourceRefs(sourceRefsJSON string) error {
	value := strings.TrimSpace(sourceRefsJSON)
	if value == "" {
		return nil
	}
	var refs []string
	if err := json.Unmarshal([]byte(value), &refs); err != nil {
		return &ValidationError{Field: "source_refs", Message: "must be a JSON array of strings"}
	}
	if len(refs) > 32 {
		return &ValidationError{Field: "source_refs", Message: "must not contain more than 32 references"}
	}
	for _, ref := range refs {
		if strings.TrimSpace(ref) == "" || len([]rune(ref)) > 200 {
			return &ValidationError{Field: "source_refs", Message: "references must be non-empty and at most 200 characters"}
		}
	}
	return nil
}

func ValidateCreateAgent(input CreateAgentInput) error {
	if strings.TrimSpace(input.Name) == "" {
		return &ValidationError{Field: "name", Message: "is required"}
	}
	if len([]rune(input.Name)) > 120 || len([]rune(input.Version)) > 64 || len([]rune(input.Instructions)) > 50000 || len(input.ConfigJSON) > 128*1024 {
		return &ValidationError{Field: "agent", Message: "agent configuration exceeds size limit"}
	}
	if input.Version == "" {
		return &ValidationError{Field: "version", Message: "is required"}
	}
	if strings.TrimSpace(input.Instructions) == "" {
		return &ValidationError{Field: "instructions", Message: "is required"}
	}
	if _, err := json.Marshal(map[string]any{}); err != nil {
		return err
	}
	var config map[string]any
	if strings.TrimSpace(input.ConfigJSON) == "" {
		input.ConfigJSON = "{}"
	}
	if err := json.Unmarshal([]byte(input.ConfigJSON), &config); err != nil {
		return &ValidationError{Field: "config_json", Message: "must be valid JSON"}
	}
	for _, key := range []string{"allow_shell", "unrestricted_network", "secret_values"} {
		if value, ok := config[key]; ok && value == true {
			return &ValidationError{Field: "config_json", Message: fmt.Sprintf("%s requires an explicit project approval", key)}
		}
	}
	if raw, ok := config["allowed_capabilities"]; ok {
		items, ok := raw.([]any)
		if !ok || len(items) > 100 {
			return &ValidationError{Field: "config_json.allowed_capabilities", Message: "must be an array with at most 100 entries"}
		}
		for _, item := range items {
			if name, ok := item.(string); !ok || strings.TrimSpace(name) == "" || len(name) > 200 {
				return &ValidationError{Field: "config_json.allowed_capabilities", Message: "entries must be non-empty strings"}
			}
		}
	}
	if raw, ok := config["memory_scope"]; ok {
		memoryScope, ok := raw.(string)
		if !ok || (memoryScope != "private" && memoryScope != "project") {
			return &ValidationError{Field: "config_json.memory_scope", Message: "must be private or project"}
		}
	}
	if raw, ok := config["budget"]; ok {
		budget, ok := raw.(map[string]any)
		if !ok {
			return &ValidationError{Field: "config_json.budget", Message: "must be an object"}
		}
		for _, key := range []string{"max_model_calls", "max_tokens"} {
			if value, exists := budget[key]; exists {
				number, ok := value.(float64)
				if !ok || number < 1 || number != float64(int64(number)) {
					return &ValidationError{Field: "config_json.budget." + key, Message: "must be a positive integer"}
				}
				if key == "max_model_calls" && number > 1000 || key == "max_tokens" && number > 10_000_000 {
					return &ValidationError{Field: "config_json.budget." + key, Message: "exceeds maximum"}
				}
			}
		}
	}
	return nil
}

type IdempotencyConflictError struct{}

func (*IdempotencyConflictError) Error() string {
	return "idempotency key was already used with a different request"
}

type StateConflictError struct {
	Status  string
	Command string
}

type ResourceConflictError struct {
	Resource string
	Reason   string
}

func (e *ResourceConflictError) Error() string {
	return fmt.Sprintf("%s conflicts with an existing resource: %s", e.Resource, e.Reason)
}

type CapacityExceededError struct {
	LimitBytes int64
}

func (e *CapacityExceededError) Error() string {
	return fmt.Sprintf("project asset capacity exceeds %d bytes", e.LimitBytes)
}

func (e *StateConflictError) Error() string {
	return fmt.Sprintf("cannot %s a task in %s state", e.Command, e.Status)
}

type ValidationError struct {
	Field   string
	Message string
}

type ForbiddenError struct {
	Message string
}

func (e *ForbiddenError) Error() string { return e.Message }

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func ValidateCreateProject(input CreateProjectInput) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		return &ValidationError{Field: "name", Message: "is required"}
	}
	if len([]rune(input.Name)) > 120 {
		return &ValidationError{Field: "name", Message: "must not exceed 120 characters"}
	}
	if len([]rune(input.Description)) > 2000 {
		return &ValidationError{Field: "description", Message: "must not exceed 2000 characters"}
	}
	if input.Kind != ProjectKindPersonal && input.Kind != ProjectKindTeam {
		return &ValidationError{Field: "kind", Message: "must be personal or team"}
	}
	if input.Scene != SceneWork && input.Scene != SceneCode {
		return &ValidationError{Field: "scene", Message: "must be work or code"}
	}
	for field, value := range map[string]string{"default_model": input.DefaultModel, "fallback_model": input.FallbackModel} {
		if len([]rune(strings.TrimSpace(value))) > 200 {
			return &ValidationError{Field: field, Message: "must not exceed 200 characters"}
		}
		if strings.ContainsAny(value, "\r\n") {
			return &ValidationError{Field: field, Message: "must be a single-line model id"}
		}
	}
	return nil
}

func ValidateCreateTask(input CreateTaskInput) error {
	input.Goal = strings.TrimSpace(input.Goal)
	if input.Goal == "" {
		return &ValidationError{Field: "goal", Message: "is required"}
	}
	if len([]rune(input.Goal)) > 20000 {
		return &ValidationError{Field: "goal", Message: "must not exceed 20000 characters"}
	}
	if len([]rune(input.Title)) > 200 {
		return &ValidationError{Field: "title", Message: "must not exceed 200 characters"}
	}
	if len([]rune(input.Acceptance)) > 10000 {
		return &ValidationError{Field: "acceptance", Message: "must not exceed 10000 characters"}
	}
	if input.Mode != ModeAnswer && input.Mode != ModeSingleAgent && input.Mode != ModeAgentTeam {
		return &ValidationError{Field: "mode", Message: "is not supported"}
	}
	if input.Visibility != VisibilityPrivate && input.Visibility != VisibilitySelected && input.Visibility != VisibilityProject {
		return &ValidationError{Field: "visibility", Message: "is not supported"}
	}
	if input.PermissionMode == "" {
		input.PermissionMode = PermissionModeAsk
	}
	if input.PermissionMode != PermissionModeDefault && input.PermissionMode != PermissionModeAsk && input.PermissionMode != PermissionModeAutoReview && input.PermissionMode != PermissionModeFullAccess {
		return &ValidationError{Field: "permission_mode", Message: "must be ask, auto_review, or full_access"}
	}
	if input.SceneOverride != "" && input.SceneOverride != SceneWork && input.SceneOverride != SceneCode {
		return &ValidationError{Field: "scene", Message: "must be work or code when overridden"}
	}
	if input.MaxModelCalls < 0 || input.MaxModelCalls > 1000 {
		return &ValidationError{Field: "max_model_calls", Message: "must be between 0 and 1000"}
	}
	if input.MaxTokens < 0 || input.MaxTokens > 10_000_000 {
		return &ValidationError{Field: "max_tokens", Message: "must be between 0 and 10000000"}
	}
	if len(input.ContextAssetIDs) > 8 {
		return &ValidationError{Field: "context_asset_ids", Message: "must contain at most 8 assets"}
	}
	if len(input.ContextMemoryIDs) > 8 {
		return &ValidationError{Field: "context_memory_ids", Message: "must contain at most 8 memories"}
	}
	if len([]rune(input.SkillID)) > 200 {
		return &ValidationError{Field: "skill_id", Message: "must not exceed 200 characters"}
	}
	if len([]rune(input.SkillArgs)) > 10000 {
		return &ValidationError{Field: "skill_args", Message: "must not exceed 10000 characters"}
	}
	if strings.TrimSpace(input.SkillArgs) != "" && strings.TrimSpace(input.SkillID) == "" {
		return &ValidationError{Field: "skill_args", Message: "requires skill_id"}
	}
	if strings.TrimSpace(input.SkillID) != "" && input.Mode != ModeSingleAgent {
		return &ValidationError{Field: "skill_id", Message: "explicit Skill invocation requires single_agent mode"}
	}
	if len(input.TeamRoles) > 3 {
		return &ValidationError{Field: "team_roles", Message: "must contain at most 3 worker roles"}
	}
	if len(input.TeamRoles) > 0 && input.Mode != ModeAgentTeam {
		return &ValidationError{Field: "team_roles", Message: "team roles require agent_team mode"}
	}
	seenRoles := map[string]struct{}{}
	for index, role := range input.TeamRoles {
		roleName := strings.TrimSpace(role.Role)
		displayName := strings.TrimSpace(role.Name)
		if roleName == "" || displayName == "" {
			return &ValidationError{Field: fmt.Sprintf("team_roles[%d]", index), Message: "role and name are required"}
		}
		if _, exists := seenRoles[roleName]; exists {
			return &ValidationError{Field: "team_roles", Message: "role names must be unique"}
		}
		if roleName == "coordinator" {
			return &ValidationError{Field: fmt.Sprintf("team_roles[%d].role", index), Message: "coordinator is reserved"}
		}
		seenRoles[roleName] = struct{}{}
	}
	return nil
}

func ValidateCreateTeamDefinition(input CreateTeamDefinitionInput) error {
	if strings.TrimSpace(input.Name) == "" || len([]rune(input.Name)) > 120 {
		return &ValidationError{Field: "name", Message: "is required and must not exceed 120 characters"}
	}
	if strings.TrimSpace(input.Version) == "" || len([]rune(input.Version)) > 64 {
		return &ValidationError{Field: "version", Message: "is required and must not exceed 64 characters"}
	}
	if len(input.Roles) == 0 || len(input.Roles) > 3 {
		return &ValidationError{Field: "roles", Message: "must contain 1 to 3 worker roles"}
	}
	if err := ValidateCreateTask(CreateTaskInput{Goal: "team definition", Mode: ModeAgentTeam, Visibility: VisibilityPrivate, TeamRoles: input.Roles}); err != nil {
		return err
	}
	if len(input.ConfigJSON) > 64*1024 {
		return &ValidationError{Field: "config_json", Message: "must not exceed 64 KiB"}
	}
	return nil
}

func ValidateCreateSchedule(input CreateScheduleInput) error {
	if strings.TrimSpace(input.Name) == "" {
		return &ValidationError{Field: "name", Message: "is required"}
	}
	if len([]rune(input.Name)) > 120 {
		return &ValidationError{Field: "name", Message: "must not exceed 120 characters"}
	}
	if strings.TrimSpace(input.CronExpression) == "" {
		return &ValidationError{Field: "cron_expression", Message: "is required"}
	}
	if len([]rune(input.CronExpression)) > 120 {
		return &ValidationError{Field: "cron_expression", Message: "must not exceed 120 characters"}
	}
	if strings.TrimSpace(input.Timezone) == "" {
		return &ValidationError{Field: "timezone", Message: "is required"}
	}
	if len([]rune(input.Goal)) == 0 || len([]rune(input.Goal)) > 20000 {
		return &ValidationError{Field: "goal", Message: "is required and must not exceed 20000 characters"}
	}
	if len([]rune(input.Acceptance)) > 10000 {
		return &ValidationError{Field: "acceptance", Message: "must not exceed 10000 characters"}
	}
	if input.Mode != ModeAnswer && input.Mode != ModeSingleAgent && input.Mode != ModeAgentTeam {
		return &ValidationError{Field: "mode", Message: "is not supported"}
	}
	if input.MisfirePolicy == "" {
		input.MisfirePolicy = ScheduleMisfireAsk
	}
	if input.MisfirePolicy != ScheduleMisfireSkip && input.MisfirePolicy != ScheduleMisfireRunNow && input.MisfirePolicy != ScheduleMisfireAsk {
		return &ValidationError{Field: "misfire_policy", Message: "must be skip, run_now, or ask"}
	}
	if input.OverlapPolicy == "" {
		input.OverlapPolicy = ScheduleOverlapForbid
	}
	if input.OverlapPolicy != ScheduleOverlapForbid && input.OverlapPolicy != ScheduleOverlapQueue && input.OverlapPolicy != ScheduleOverlapReplace {
		return &ValidationError{Field: "overlap_policy", Message: "must be forbid, queue, or replace"}
	}
	if input.MisfireGraceSeconds < 0 || input.MisfireGraceSeconds > 7*24*60*60 {
		return &ValidationError{Field: "misfire_grace_seconds", Message: "must be between 0 and 604800"}
	}
	return ValidateAssetVisibility(input.Visibility)
}

func ValidateControlTask(input ControlTaskInput) error {
	if input.Command != TaskCommandPause && input.Command != TaskCommandResume && input.Command != TaskCommandCancel && input.Command != TaskCommandRetry {
		return &ValidationError{Field: "command", Message: "must be pause, resume, cancel, or retry"}
	}
	return nil
}

func ValidateDecideApproval(input DecideApprovalInput) error {
	if input.Decision != ApprovalApproved && input.Decision != ApprovalRejected {
		return &ValidationError{Field: "decision", Message: "must be approved or rejected"}
	}
	if input.Scope == "" {
		input.Scope = ApprovalScopeOnce
	}
	if input.Scope != ApprovalScopeOnce && input.Scope != ApprovalScopeConversation && input.Scope != ApprovalScopeProject {
		return &ValidationError{Field: "scope", Message: "must be once, conversation, or project"}
	}
	if input.Decision == ApprovalRejected && input.Scope != ApprovalScopeOnce {
		return &ValidationError{Field: "scope", Message: "rejected decisions must use once"}
	}
	if input.Decision == ApprovalApproved && input.Scope == ApprovalScopeProject && !input.Confirmed {
		return &ValidationError{Field: "confirmed", Message: "must be true for project approval rules"}
	}
	if len([]rune(input.Note)) > 1000 {
		return &ValidationError{Field: "note", Message: "must not exceed 1000 characters"}
	}
	return nil
}

func ValidateResolveSideEffect(input ResolveSideEffectInput) error {
	if input.Decision != SideEffectDecisionConfirmApplied && input.Decision != SideEffectDecisionConfirmNotApplied && input.Decision != SideEffectDecisionAbandon {
		return &ValidationError{Field: "decision", Message: "must be confirm_applied, confirm_not_applied, or abandon"}
	}
	if len([]rune(input.Note)) > 2000 {
		return &ValidationError{Field: "note", Message: "must not exceed 2000 characters"}
	}
	if input.Decision != SideEffectDecisionConfirmApplied && strings.TrimSpace(input.Note) == "" {
		return &ValidationError{Field: "note", Message: "is required when the side effect is not confirmed applied"}
	}
	return nil
}

func ValidateCreatePlanItem(input CreatePlanItemInput) error {
	if strings.TrimSpace(input.Title) == "" {
		return &ValidationError{Field: "title", Message: "is required"}
	}
	if len([]rune(input.Title)) > 200 {
		return &ValidationError{Field: "title", Message: "must not exceed 200 characters"}
	}
	if len([]rune(input.Description)) > 2000 {
		return &ValidationError{Field: "description", Message: "must not exceed 2000 characters"}
	}
	if input.Visibility != VisibilityPrivate && input.Visibility != VisibilitySelected && input.Visibility != VisibilityProject {
		return &ValidationError{Field: "visibility", Message: "is not supported"}
	}
	return nil
}

func ValidateMovePlanItem(input MovePlanItemInput) error {
	if input.Status != PlanStatusTodo && input.Status != PlanStatusInProgress && input.Status != PlanStatusPaused && input.Status != PlanStatusDone {
		return &ValidationError{Field: "status", Message: "must be todo, in_progress, paused, or done"}
	}
	return nil
}

func ValidateAssetName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return &ValidationError{Field: "name", Message: "is required"}
	}
	if len([]rune(name)) > 255 {
		return &ValidationError{Field: "name", Message: "must not exceed 255 characters"}
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return &ValidationError{Field: "name", Message: "must be a single safe path segment"}
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return &ValidationError{Field: "name", Message: "must not contain control characters"}
		}
	}
	return nil
}

func ValidateAssetVisibility(visibility string) error {
	if visibility != VisibilityPrivate && visibility != VisibilitySelected && visibility != VisibilityProject {
		return &ValidationError{Field: "visibility", Message: "is not supported"}
	}
	return nil
}

func ValidateCreateAssetFolder(input CreateAssetFolderInput) error {
	if err := ValidateAssetName(input.Name); err != nil {
		return err
	}
	return ValidateAssetVisibility(input.Visibility)
}

func ValidateUploadAsset(input UploadAssetInput, maxBytes int64) error {
	if err := ValidateAssetName(input.Name); err != nil {
		return err
	}
	if err := ValidateAssetVisibility(input.Visibility); err != nil {
		return err
	}
	if input.SizeHint < 0 {
		return &ValidationError{Field: "file", Message: "size must not be negative"}
	}
	if input.SizeHint > maxBytes {
		return &ValidationError{Field: "file", Message: fmt.Sprintf("must not exceed %d bytes", maxBytes)}
	}
	if len([]rune(input.MediaType)) > 255 {
		return &ValidationError{Field: "media_type", Message: "must not exceed 255 characters"}
	}
	return nil
}

func ValidatePublishArtifact(input PublishArtifactInput) error {
	if err := ValidateAssetName(input.Name); err != nil {
		return err
	}
	return ValidateAssetVisibility(input.Visibility)
}

func ValidateAddProjectMember(input AddProjectMemberInput) error {
	if input.Email == "" {
		return &ValidationError{Field: "email", Message: "is required"}
	}
	parsed, err := mail.ParseAddress(input.Email)
	if err != nil || !strings.EqualFold(parsed.Address, input.Email) {
		return &ValidationError{Field: "email", Message: "must be a valid bare email address"}
	}
	if len(input.Email) > 254 {
		return &ValidationError{Field: "email", Message: "must not exceed 254 characters"}
	}
	if input.DisplayName == "" {
		return &ValidationError{Field: "display_name", Message: "is required"}
	}
	if len([]rune(input.DisplayName)) > 120 {
		return &ValidationError{Field: "display_name", Message: "must not exceed 120 characters"}
	}
	if input.Role != ProjectRoleOwner && input.Role != ProjectRoleMember && input.Role != ProjectRoleApprover {
		return &ValidationError{Field: "role", Message: "must be owner, member, or approver"}
	}
	return nil
}

func ValidateCreateProjectInvitation(input CreateProjectInvitationInput) error {
	if _, err := mail.ParseAddress(input.Email); err != nil || !strings.Contains(input.Email, "@") {
		return &ValidationError{Field: "email", Message: "must be a valid email"}
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		return &ValidationError{Field: "display_name", Message: "is required"}
	}
	if len([]rune(input.DisplayName)) > 120 {
		return &ValidationError{Field: "display_name", Message: "must not exceed 120 characters"}
	}
	if input.Role != ProjectRoleMember && input.Role != ProjectRoleApprover && input.Role != ProjectRoleOwner {
		return &ValidationError{Field: "role", Message: "must be member, approver, or owner"}
	}
	if input.ExpiresIn <= 0 || input.ExpiresIn > 30*24*60*60 {
		return &ValidationError{Field: "expires_in", Message: "must be between 1 second and 30 days"}
	}
	return nil
}
