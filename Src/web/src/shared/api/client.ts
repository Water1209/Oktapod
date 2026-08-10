export type Profile = 'personal' | 'team'
export type ProjectKind = 'personal' | 'team'
export type Scene = 'work' | 'code'
export type TaskMode = 'auto' | 'answer' | 'single_agent' | 'agent_team'
export type PermissionMode = 'ask' | 'auto_review' | 'full_access'
export type Visibility = 'private' | 'selected' | 'project'
export type PlanStatus = 'todo' | 'in_progress' | 'paused' | 'done'
export type ScheduleStatus = 'enabled' | 'disabled'
export type ScheduleMisfirePolicy = 'skip' | 'run_now' | 'ask'
export type ScheduleOverlapPolicy = 'forbid' | 'queue' | 'replace'
export type MemoryStatus = 'active' | 'inactive'
export type CapabilityKind = 'skill' | 'plugin' | 'mcp' | 'agent'
export type CapabilityStatus = 'enabled' | 'disabled' | 'quarantined'
export type CapabilityScope = 'global' | 'project'
export type ResourceAvailability = 'available' | 'degraded' | 'disabled' | 'quarantined'
export type TaskStatus =
  | 'queued'
  | 'running'
  | 'waiting_approval'
  | 'waiting_input'
  | 'paused'
  | 'blocked'
  | 'succeeded'
  | 'failed'
  | 'canceled'

export interface Bootstrap {
  profile: Profile
  model_endpoint: string
  model_configured: boolean
  user: { id: string; username?: string; display_name: string }
  organization: { id: string; name: string }
  capabilities: {
    os: string
    sandbox: string
    filesystem: string
    command: string
    network: string
    browser: string
    mcp: string
  }
}

export interface AuthenticatedUser {
  user_id: string
  display_name: string
  expires_at: string
}

export interface ModelDescriptor {
  id: string
  tool_calling?: boolean
  vision?: boolean
  structured_output?: boolean
  reasoning?: boolean
  context_window?: number
}

export interface ProviderInstance {
  provider_id: string
  display_name: string
  protocol_family: 'openai_compatible' | 'anthropic' | 'gemini'
  endpoint: string
  default_model_id: string
  status: 'active' | 'disabled' | 'error'
  is_default: boolean
  credential_configured: boolean
  created_at: string
  updated_at: string
}

export interface ProviderModelCatalogEntry {
  provider_id: string
  model_id: string
  capabilities: Record<string, unknown>
  context_window: number
  max_output: number
  thinking_map: Record<string, unknown>
  pricing: Record<string, unknown>
  fetched_at: string
  expires_at: string
  catalog_version: string
  stale: boolean
  last_error?: string
}

export interface ProviderTestResult {
  provider_id: string
  stage: string
  latency_millis: number
  catalog_status: string
  model_count: number
  redacted_error: string
  completed_at: string
}

export interface Diagnostics {
  status: 'ok' | 'degraded'
  version: string
  profile: string
  database: { name: string; status: 'ok' | 'degraded' }
  providers: { total: number; active: number; stale_catalog: number }
  worker: { queued_runs: number; running_runs: number; waiting_approvals: number; active_leases: number }
  sse: { active_connections: number }
  logs: { available: boolean; redacted: boolean; detail: string }
  maintenance: { backup_endpoint_available: boolean; restore_endpoint_available: boolean; detail: string }
}

export interface Project {
  id: string
  organization_id: string
  name: string
  description?: string
  kind: ProjectKind
  scene: Scene
  default_model: string
  fallback_model: string
  max_model_calls: number
  max_tokens: number
  created_at: string
  updated_at: string
}

export interface ProjectMember {
  user_id: string
  display_name: string
  email: string
  role: 'owner' | 'member' | 'approver'
  status: 'active' | 'removed'
  created_at: string
  updated_at: string
}

export interface ProjectInvitation {
  id: string
  organization_id: string
  project_id: string
  invited_by: string
  email: string
  display_name: string
  role: 'owner' | 'member' | 'approver'
  status: 'pending' | 'accepted' | 'revoked' | 'expired'
  accepted_user_id?: string
  expires_at: string
  created_at: string
  updated_at: string
  invite_token?: string
  session_token?: string
}

export interface Task {
  id: string
  project_id: string
  run_id: string
  conversation_id: string
  title: string
  goal: string
  acceptance?: string
  mode: TaskMode
  visibility: Visibility
  status: TaskStatus
  error_code?: string
  result_summary?: string
  accepted: boolean
  created_at: string
  updated_at: string
}

export interface Schedule {
  id: string
  organization_id: string
  project_id: string
  owner_user_id: string
  name: string
  cron_expression: string
  timezone: string
  goal: string
  acceptance: string
  mode: Exclude<TaskMode, 'auto'>
  visibility: Visibility
  status: ScheduleStatus
  misfire_policy: ScheduleMisfirePolicy
  overlap_policy: ScheduleOverlapPolicy
  misfire_grace_seconds: number
  next_run_at: string
  created_at: string
  updated_at: string
}

export interface Capability {
  id: string
  organization_id: string
  project_id: string
  owner_user_id: string
  kind: CapabilityKind
  name: string
  version: string
  manifest_json: string
  content_hash: string
  status: CapabilityStatus
  availability: ResourceAvailability
  risk_level: string
  permission_diff: string[]
  scope_type: CapabilityScope
  scope_id: string
  source_filename: string
  bundle_hash: string
  install_job_id: string
  credential_configured: boolean
  created_at: string
  updated_at: string
}

export interface MCPHealth {
  id: string
  capability_id: string
  project_id: string
  transport: 'stdio' | 'streamable_http'
  status: 'disabled' | 'needs-auth' | 'connected' | 'failed'
  protocol: string
  tool_count: number
  tools_json: string
  last_error: string
  connected_at?: string
  updated_at: string
}

export interface Memory {
  id: string
  organization_id: string
  project_id: string
  owner_user_id: string
  kind: string
  title: string
  content: string
  source_hash: string
  source_refs: string[]
  visibility: Visibility
  status: MemoryStatus
  created_at: string
  updated_at: string
}

export interface AgentDefinition {
  id: string
  organization_id: string
  project_id: string
  owner_user_id: string
  name: string
  version: string
  instructions: string
  model: string
  fallback_model: string
  config_json: string
  status: CapabilityStatus
  created_at: string
  updated_at: string
}

export interface TeamDefinition {
  id: string
  project_id: string
  owner_user_id: string
  name: string
  version: string
  status: CapabilityStatus
  roles: { role: string; name: string }[]
  config_json: string
  created_at: string
  updated_at: string
}

export interface Conversation {
  id: string
  project_id: string
  title: string
  visibility: Visibility
  message_count: number
  last_message: string
  created_at: string
  updated_at: string
  active_leaf_entry_id?: string
  parent_session_id?: string
  forked_from_entry_id?: string
}

export interface MCPOAuthSession {
  id: string
  capability_id: string
  authorization_url?: string
  status: 'preparing' | 'waiting' | 'connected' | 'canceled' | 'failed'
  last_error?: string
  expires_at: string
  updated_at: string
}

export interface SessionEntry {
  id: string
  project_id: string
  conversation_id: string
  parent_entry_id: string
  entry_seq: number
  kind: 'message' | 'config_change' | 'compaction' | 'custom' | 'branch_summary'
  ref_id: string
  label: string
  summary: string
  namespace: string
  payload: Record<string, unknown>
  schema_version: number
  context_injection: 'default' | 'include' | 'exclude' | 'summary_only'
  created_at: string
}

export interface SessionTreePage {
  items: SessionEntry[]
  active_leaf_entry_id: string
  next_entry_seq: number
}

export interface ConversationMessage {
  id: string
  project_id: string
  conversation_id: string
  task_id: string | null
  server_seq: number
  role: 'user' | 'assistant' | 'system' | 'tool'
  visibility: Visibility
  content: string
  created_at: string
}

export interface ConversationQueueItem {
  id: string
  organization_id?: string
  project_id?: string
  conversation_id: string
  active_task_id: string
  content: string
  input_json?: string
  kind: 'follow_up' | 'steering'
  status: 'pending' | 'claimed' | 'consumed' | 'canceled'
  created_at: string
  claimed_at?: string
  consumed_at?: string
}

export interface ProjectTurnResult {
  route: 'chat' | 'task' | 'resource'
  conversation: Conversation
  messages: ConversationMessage[]
  task?: Task
  queued?: boolean
  queue_item?: ConversationQueueItem
  resource_install_draft?: ResourceInstallDraft
}

export interface ProjectTurnInput {
  conversation_id?: string
  content: string
  acceptance?: string
  mode?: TaskMode
  visibility?: Visibility
  permission_mode?: PermissionMode | 'default'
  queue_kind?: 'follow_up' | 'steering'
  scene?: Scene
  provider_id?: string
  model?: string
  fallback_model?: string
  thinking?: 'auto' | 'off' | 'low' | 'medium' | 'high'
  max_model_calls?: number
  max_tokens?: number
  agent_id?: string
  team_definition_id?: string
  team_roles?: { role: string; name: string }[]
  context_asset_ids?: string[]
  context_memory_ids?: string[]
	skill_id?: string
	skill_args?: string
}

export interface StreamProjectTurnHandlers {
  onRoute?: (route: 'chat' | 'task' | 'resource') => void
  onDelta?: (delta: string) => void
}

export interface ProjectActivity {
  id: string
  project_id: string
  run_id: string
  task_id: string
  task_title: string
  task_status: TaskStatus
  mode: Exclude<TaskMode, 'auto'>
  event_type: string
  visibility: Visibility
  payload: Record<string, unknown>
  occurred_at: string
}

export interface PlanItem {
  id: string
  project_id: string
  title: string
  description: string
  status: PlanStatus
  source: 'manual' | 'task'
  linked_task_id?: string
  visibility: Visibility
  rank: number
  created_at: string
  updated_at: string
}

export interface RunEvent {
  id: string
  run_id: string
  stream_seq: number
  event_type: string
  occurred_at: string
  payload: Record<string, unknown>
}

export interface AgentTeamMember {
  id: string
  run_id: string
  role: 'coordinator' | 'researcher' | 'builder' | 'reviewer'
  display_name: string
  status: 'planned' | 'working' | 'waiting' | 'done' | 'failed'
  current_step_id: string
  model: string
  created_at: string
  updated_at: string
}

export interface AgentTeamMessage {
  id: string
  run_id: string
  sender_agent_id: string
  server_seq: number
  message_type: 'plan' | 'handoff' | 'result' | 'review' | 'synthesis'
  content: string
  content_hash: string
  created_at: string
}

export interface AgentTeamRoom {
  run_id: string
  members: AgentTeamMember[]
  messages: AgentTeamMessage[]
  next_cursor: number
}

export interface RunUsage {
  run_id: string
  model_calls: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  reasoning_tokens: number
  estimated_calls: number
  latency_ms: number
  cost_status: 'unpriced' | 'estimated' | 'confirmed'
  currency: string
  cost_decimal: string
  budget_max_calls: number
  budget_max_tokens: number
  budget_status: 'active' | 'exhausted' | 'approved_exception'
  remaining_calls: number
  remaining_tokens: number
  reserved_calls: number
  reserved_tokens: number
}

export interface RunChanges {
  run_id: string
  project_id: string
  baseline: 'run_start' | 'head' | 'index'
  baseline_head: string
  current_head: string
  dirty_at_start: boolean
  status_hash: string
  diff: string
  truncated: boolean
  baseline_notice: string
}

export interface UsageBucket {
  key: string
  label: string
  model_calls: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  reasoning_tokens: number
  estimated_calls: number
  latency_ms: number
  cost_decimal: string
}

export interface ProjectUsage {
  project_id: string
  model_calls: number
  input_tokens: number
  output_tokens: number
  cached_tokens: number
  reasoning_tokens: number
  estimated_calls: number
  latency_ms: number
  cost_status: 'unpriced' | 'estimated' | 'confirmed'
  currency: string
  cost_decimal: string
  by_day: UsageBucket[]
  by_model: UsageBucket[]
  by_member: UsageBucket[]
  by_agent: UsageBucket[]
}

export interface AdvisorReport {
  id: string
  run_id: string
  task_id: string
  status: 'preview' | 'completed' | 'rejected'
  trigger_reason: string
  summary: string
  evidence: string
  estimated_model_calls: number
  estimated_tokens: number
  estimated_cost_decimal: string
  created_at: string
}

export interface Artifact {
  id: string
  project_id: string
  task_id: string
  run_id: string
  step_id: string
  name: string
  kind: string
  media_type: string
  content_hash: string
  size_bytes: number
  visibility: Visibility
  source_type: string
  created_at: string
}

export interface Asset {
  id: string
  project_id: string
  owner_user_id: string
  parent_id: string
  name: string
  kind: 'file' | 'folder'
  media_type: string
  content_hash: string
  size_bytes: number
  visibility: Visibility
  source_type: 'upload' | 'artifact'
  source_artifact_id: string
  created_at: string
  updated_at: string
}

export interface AssetCollection {
  items: Asset[]
  visible_bytes: number
  quota_bytes: number
}

export interface Approval {
  id: string
  project_id: string
  task_id: string
  run_id: string
  step_id: string
  tool_call_id: string
  tool_name: string
  risk_level: 'L0' | 'L1' | 'L2' | 'L3'
  status: 'pending' | 'approved' | 'rejected'
  reason: string
  frozen_params: Record<string, unknown>
  input_hash: string
  requested_by: string
  decided_by?: string
  requested_at: string
  expires_at: string
  decided_at?: string
}

export type ApprovalScope = 'once' | 'conversation' | 'project'

export interface RunPlanItem {
  id: string
  plan_revision_id: string
  position: number
  title: string
  description: string
  status: 'pending' | 'running' | 'waiting_user' | 'completed' | 'blocked' | 'canceled'
  result_summary: string
  started_at?: string
  completed_at?: string
  created_at: string
  updated_at: string
}

export interface MCPDefinitionV1 {
  schema_version: 'oktapod.mcp/v1'
  source: { type: 'official' | 'registry' | 'npm' | 'pypi' | 'url' | 'local' | 'manual'; reference?: string; version?: string; publisher?: string }
  transport: { type: 'stdio'; command: string; args?: string[]; auth_env?: string } | { type: 'streamable_http'; url: string }
  auth: { type: 'none' | 'environment' | 'bearer' | 'oauth' | 'custom_header'; header?: string }
  secret_bindings?: Array<{ name: string; target: string; required: boolean }>
  headers?: Record<string, string>
  permissions?: string[]
  description?: string
}

export interface MCPInstallCandidate {
  name: string
  title?: string
  version: string
  description?: string
  repository_url?: string
  mcp_definition: MCPDefinitionV1
}

export interface ResourceInstallDraft {
  id: string
  kind: 'skill' | 'mcp' | 'agent'
  source_input: string
  resolved_name?: string
  resolved_version?: string
  mcp_definition?: MCPDefinitionV1
  candidates?: MCPInstallCandidate[]
  status: 'draft' | 'needs_secret' | 'validated' | 'rejected' | 'installed'
  warnings: string[]
  required_secrets: Array<{ name: string; target: string; required: boolean }>
  created_at: string
  updated_at: string
}

export interface RunPlanRevision {
  id: string
  project_id: string
  task_id: string
  run_id: string
  revision: number
  trigger: 'initial' | 'steering' | 'follow_up' | 'permission_change' | 'recovery'
  goal_summary: string
  status: 'active' | 'superseded' | 'completed' | 'blocked' | 'canceled'
  supersedes_revision_id: string
  created_by: string
  created_at: string
  superseded_at?: string
  items: RunPlanItem[]
}

export interface HumanAction {
  id: string
  project_id: string
  conversation_id: string
  task_id: string
  run_id: string
  step_id: string
  plan_revision_id: string
  run_plan_item_id: string
  kind: 'tool_approval' | 'permission_escalation' | 'input_request' | 'budget_request' | 'side_effect_resolution'
  status: 'reviewing' | 'waiting_user' | 'approved' | 'rejected' | 'answered' | 'expired' | 'superseded'
  reviewer: 'user' | 'auto'
  risk_level: string
  title: string
  reason: string
  request: Record<string, unknown>
  request_hash: string
  response: Record<string, unknown>
  requested_by: string
  decided_by: string
  requested_at: string
  expires_at: string
  decided_at?: string
  version: number
}

export interface ApprovalRule {
  id: string
  project_id: string
  tool_name: string
  rule_key: string
  created_from_approval_id: string
  created_by: string
  created_at: string
  expires_at: string
  revoked_at?: string
  revoked_by: string
}

export interface SideEffectReceipt {
  id: string
  project_id: string
  task_id: string
  run_id: string
  step_id: string
  tool_call_id: string
  tool_name: string
  input_hash: string
  idempotent: boolean
  status: 'started' | 'succeeded' | 'failed' | 'unknown'
  error_code: string
  started_at: string
  finished_at?: string
}

export interface InboxItem {
  id: string
  kind: 'approval' | 'task_input' | 'task_recovery' | 'side_effect_unknown'
  project_id: string
  task_id: string
  title: string
  reason: string
  status: string
  updated_at: string
}

export interface InboxHistoryItem {
  id: string
  kind: 'approval' | 'side_effect' | 'task'
  project_id: string
  task_id: string
  title: string
  summary: string
  status: string
  occurred_at: string
}

export interface ProblemDetails {
  type: string
  title: string
  status: number
  detail?: string
  code: string
  correlation_id: string
}

export class ApiProblem extends Error {
  readonly problem: ProblemDetails

  constructor(problem: ProblemDetails) {
    super(problem.detail || problem.title)
    this.name = 'ApiProblem'
    this.problem = problem
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	const method = (init?.method || 'GET').toUpperCase()
	const csrf = (typeof document === 'undefined' ? '' : document.cookie)
		.split('; ')
		.find((item) => item.startsWith('oktapod_csrf='))
		?.slice('oktapod_csrf='.length)
	const response = await fetch(`/api/v1${path}`, {
    credentials: 'same-origin',
    ...init,
    headers: {
      Accept: 'application/json',
      ...(typeof init?.body === 'string' ? { 'Content-Type': 'application/json' } : {}),
		...(csrf && ['POST', 'PUT', 'PATCH', 'DELETE'].includes(method) ? { 'X-CSRF-Token': decodeURIComponent(csrf) } : {}),
      ...init?.headers,
    },
  })
  if (!response.ok) {
    let problem: ProblemDetails
    try {
      problem = (await response.json()) as ProblemDetails
    } catch {
      problem = {
        type: 'about:blank',
        title: '请求失败',
        status: response.status,
        code: 'HTTP_ERROR',
        correlation_id: response.headers.get('X-Request-ID') || 'unknown',
      }
    }
    throw new ApiProblem(problem)
  }
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

async function streamProjectTurn(path: string, input: ProjectTurnInput, handlers: StreamProjectTurnHandlers): Promise<ProjectTurnResult> {
	const csrf = (typeof document === 'undefined' ? '' : document.cookie)
		.split('; ')
		.find((item) => item.startsWith('oktapod_csrf='))
		?.slice('oktapod_csrf='.length)
	const response = await fetch(`/api/v1${path}`, {
		method: 'POST',
		credentials: 'same-origin',
		headers: {
			Accept: 'text/event-stream',
			'Content-Type': 'application/json',
			...(csrf ? { 'X-CSRF-Token': decodeURIComponent(csrf) } : {}),
			'Idempotency-Key': crypto.randomUUID(),
		},
		body: JSON.stringify(input),
	})
	if (!response.ok) {
		let problem: ProblemDetails
		try { problem = (await response.json()) as ProblemDetails } catch { problem = { type: 'about:blank', title: '请求失败', status: response.status, code: 'HTTP_ERROR', correlation_id: response.headers.get('X-Request-ID') || 'unknown' } }
		throw new ApiProblem(problem)
	}
	if (!response.body) throw new Error('流式响应不可用')
	const reader = response.body.getReader()
	const decoder = new TextDecoder()
	let buffer = ''
	let result: ProjectTurnResult | undefined
	const consume = (block: string) => {
		let event = 'message'
		const data: string[] = []
		for (const line of block.split(/\r?\n/)) {
			if (line.startsWith('event:')) event = line.slice(6).trim()
			if (line.startsWith('data:')) data.push(line.slice(5).trim())
		}
		if (data.length === 0) return
		const payload = JSON.parse(data.join('\n')) as Record<string, unknown>
		if (event === 'route' && (payload.route === 'chat' || payload.route === 'task' || payload.route === 'resource')) handlers.onRoute?.(payload.route)
		if (event === 'assistant_delta' && typeof payload.delta === 'string') handlers.onDelta?.(payload.delta)
		if (event === 'error') throw new ApiProblem({ type: 'about:blank', title: '流式响应失败', status: 502, code: typeof payload.code === 'string' ? payload.code : 'TURN_STREAM_ERROR', detail: typeof payload.detail === 'string' ? payload.detail : '模型响应中断', correlation_id: typeof payload.correlation_id === 'string' ? payload.correlation_id : 'unknown' })
		if (event === 'done') result = payload as unknown as ProjectTurnResult
	}
	while (true) {
		const chunk = await reader.read()
		buffer += decoder.decode(chunk.value || new Uint8Array(), { stream: !chunk.done })
		const blocks = buffer.split(/\r?\n\r?\n/)
		buffer = blocks.pop() || ''
		for (const block of blocks) consume(block)
		if (chunk.done) break
	}
	if (buffer.trim()) consume(buffer)
	if (!result) throw new Error('流式响应未完成')
	return result
}

export const api = {
	login: (input: { username: string; password: string }) =>
		request<AuthenticatedUser>('/auth/login', { method: 'POST', body: JSON.stringify(input) }),
	register: (input: { username: string; display_name: string; password: string }) =>
		request<AuthenticatedUser>('/auth/register', { method: 'POST', body: JSON.stringify(input) }),
	logout: () => request<void>('/auth/logout', { method: 'POST' }),
  bootstrap: () => request<Bootstrap>('/bootstrap'),
  models: () => request<{ items: ModelDescriptor[]; default_model: string }>('/models'),
  diagnostics: () => request<Diagnostics>('/diagnostics'),
  providers: () => request<{ items: ProviderInstance[] }>('/providers'),
  provider: (providerId: string) => request<ProviderInstance>(`/providers/${providerId}`),
  saveProvider: (providerId: string | undefined, input: { display_name: string; protocol_family: ProviderInstance['protocol_family']; endpoint: string; default_model_id?: string; status?: ProviderInstance['status']; is_default?: boolean; credential?: string }) =>
    request<ProviderInstance>(providerId ? `/providers/${providerId}` : '/providers', { method: providerId ? 'PUT' : 'POST', body: JSON.stringify(input) }),
  deleteProvider: (providerId: string) => request<void>(`/providers/${providerId}`, { method: 'DELETE' }),
  testProvider: (providerId: string) => request<ProviderTestResult>(`/providers/${providerId}/test`, { method: 'POST' }),
  providerModels: (providerId: string) => request<{ items: ProviderModelCatalogEntry[] }>(`/providers/${providerId}/models`),
  projects: () => request<{ items: Project[] }>('/projects'),
  createProject: (input: { name: string; description: string; kind: ProjectKind; scene: Scene; default_model?: string; fallback_model?: string }) =>
    request<Project>('/projects', {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify(input),
    }),
  project: (projectId: string) => request<Project>(`/projects/${projectId}`),
  projectUsage: (projectId: string, since?: string) => request<ProjectUsage>(`/projects/${projectId}/usage${since ? `?since=${encodeURIComponent(since)}` : ''}`),
  updateProjectSettings: (projectId: string, input: { default_model?: string; fallback_model?: string; max_model_calls?: number; max_tokens?: number }) =>
    request<Project>(`/projects/${projectId}`, { method: 'PATCH', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(input) }),
  projectMembers: (projectId: string) => request<{ items: ProjectMember[] }>(`/projects/${projectId}/members`),
  projectInvitations: (projectId: string) => request<{ items: ProjectInvitation[] }>(`/projects/${projectId}/invitations`),
  createProjectInvitation: (projectId: string, input: { email: string; display_name: string; role: ProjectInvitation['role'] }) =>
    request<ProjectInvitation>(`/projects/${projectId}/invitations`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify(input),
    }),
  acceptProjectInvitation: (token: string) =>
    request<ProjectInvitation>('/invitations/accept', {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify({ token }),
    }),
  revokeProjectInvitation: (invitationId: string) =>
    request<void>(`/invitations/${invitationId}/revoke`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
    }),
  addProjectMember: (projectId: string, input: { email: string; display_name: string; role: ProjectMember['role'] }) =>
    request<ProjectMember>(`/projects/${projectId}/members`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify(input),
    }),
  removeProjectMember: (projectId: string, userId: string) =>
    request<void>(`/projects/${projectId}/members/${userId}`, {
      method: 'DELETE',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
    }),
  tasks: (projectId: string) => request<{ items: Task[] }>(`/projects/${projectId}/tasks`),
  schedules: (projectId: string) => request<{ items: Schedule[] }>(`/projects/${projectId}/schedules`),
  createSchedule: (projectId: string, input: { name: string; cron_expression: string; timezone: string; goal: string; acceptance?: string; mode: Exclude<TaskMode, 'auto'>; visibility: Visibility; misfire_policy?: ScheduleMisfirePolicy; overlap_policy?: ScheduleOverlapPolicy; misfire_grace_seconds?: number }) =>
    request<Schedule>(`/projects/${projectId}/schedules`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(input) }),
  setScheduleStatus: (scheduleId: string, status: ScheduleStatus) =>
    request<Schedule>(`/schedules/${scheduleId}/status`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ status }) }),
  capabilities: (projectId: string) => request<{ items: Capability[] }>(`/projects/${projectId}/capabilities`),
  resolveResourceInstallDraft: (projectId: string, sourceInput: string) =>
    request<ResourceInstallDraft>(`/projects/${projectId}/resource-install-drafts:resolve`, { method: 'POST', body: JSON.stringify({ source_input: sourceInput }) }),
  createCapability: (projectId: string, input: { kind: CapabilityKind; name: string; version?: string; manifest_json: string; scope_type?: CapabilityScope; source_filename?: string; bundle_base64?: string; credential?: string; credentials?: Record<string, string> }) =>
    request<Capability>(`/projects/${projectId}/capabilities`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(input) }),
  setCapabilityStatus: (capabilityId: string, status: CapabilityStatus) =>
    request<Capability>(`/capabilities/${capabilityId}/status`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ status }) }),
  deleteCapability: (capabilityId: string) => request<void>(`/capabilities/${capabilityId}`, { method: 'DELETE' }),
  reviewCapability: (capabilityId: string, decision: 'approved' | 'rejected', note = '') =>
    request<Capability>(`/capabilities/${capabilityId}/review`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ decision, note }) }),
  connectMCP: (capabilityId: string) => request<MCPHealth>(`/capabilities/${capabilityId}/mcp/connect`, { method: 'POST' }),
  startMCPOAuth: (capabilityId: string) => request<MCPOAuthSession>(`/capabilities/${capabilityId}/mcp/oauth/start`, { method: 'POST' }),
  mcpOAuthStatus: (capabilityId: string, sessionId: string) => request<MCPOAuthSession>(`/capabilities/${capabilityId}/mcp/oauth/${sessionId}`),
  cancelMCPOAuth: (capabilityId: string, sessionId: string) => request<MCPOAuthSession>(`/capabilities/${capabilityId}/mcp/oauth/${sessionId}/cancel`, { method: 'POST' }),
  disconnectMCP: (capabilityId: string) => request<MCPHealth>(`/capabilities/${capabilityId}/mcp/disconnect`, { method: 'POST' }),
  mcpHealth: (capabilityId: string) => request<MCPHealth>(`/capabilities/${capabilityId}/mcp/health`),
  memories: (projectId: string, query = '') => request<{ items: Memory[] }>(`/projects/${projectId}/memories${query ? `?q=${encodeURIComponent(query)}` : ''}`),
  createMemory: (projectId: string, input: { kind: string; title: string; content: string; visibility: Visibility; source_refs?: string[] }) =>
    request<Memory>(`/projects/${projectId}/memories`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(input) }),
  updateMemory: (memoryId: string, input: { title: string; content: string; visibility: Visibility; source_refs: string[] }) =>
    request<Memory>(`/memories/${memoryId}`, { method: 'PATCH', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(input) }),
  setMemoryStatus: (memoryId: string, status: MemoryStatus) =>
    request<Memory>(`/memories/${memoryId}/status`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ status }) }),
  deleteMemory: (memoryId: string) =>
    request<void>(`/memories/${memoryId}`, { method: 'DELETE', headers: { 'Idempotency-Key': crypto.randomUUID() } }),
  createDailySummary: (projectId: string) => request<Memory>(`/projects/${projectId}/memories/daily-summary`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() } }),
  promoteMemoryToSkill: (memoryId: string) => request<Capability>(`/memories/${memoryId}/promote`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() } }),
  agents: (projectId: string) => request<{ items: AgentDefinition[] }>(`/projects/${projectId}/agents`),
  createAgent: (projectId: string, input: { name: string; version: string; instructions: string; model?: string; fallback_model?: string; config_json?: string }) =>
    request<AgentDefinition>(`/projects/${projectId}/agents`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(input) }),
  setAgentStatus: (agentId: string, status: CapabilityStatus) => request<AgentDefinition>(`/agents/${agentId}/status`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ status }) }),
  deleteAgent: (agentId: string) => request<void>(`/agents/${agentId}`, { method: 'DELETE' }),
  teams: (projectId: string) => request<{ items: TeamDefinition[] }>(`/projects/${projectId}/teams`),
  createTeam: (projectId: string, input: { name: string; version: string; roles: { role: string; name: string }[]; config_json?: string }) =>
    request<TeamDefinition>(`/projects/${projectId}/teams`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(input) }),
  setTeamStatus: (teamId: string, status: CapabilityStatus) => request<TeamDefinition>(`/teams/${teamId}/status`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify({ status }) }),
  conversations: (projectId: string) => request<{ items: Conversation[] }>(`/projects/${projectId}/conversations`),
  renameConversation: (conversationId: string, title: string) => request<Conversation>(`/conversations/${conversationId}`, { method: 'PATCH', body: JSON.stringify({ title }) }),
  deleteConversation: (conversationId: string) => request<void>(`/conversations/${conversationId}`, { method: 'DELETE' }),
  conversationMessages: (conversationId: string, after = 0) => request<{ items: ConversationMessage[]; next_cursor: number }>(`/conversations/${conversationId}/messages?after=${after}`),
  sessionEntries: (conversationId: string, afterEntrySeq = 0, limit = 200) => request<SessionTreePage>(`/conversations/${conversationId}/entries?after_entry_seq=${afterEntrySeq}&limit=${limit}`),
  sessionEntryPath: (entryId: string) => request<{ items: SessionEntry[] }>(`/session-entries/${entryId}/path`),
  navigateSession: (conversationId: string, entryId: string, expectedLeafEntryId: string) =>
    request<{ conversation: Conversation; entry: SessionEntry; composer_text: string }>(`/conversations/${conversationId}/navigate`, { method: 'POST', body: JSON.stringify({ entry_id: entryId, expected_leaf_entry_id: expectedLeafEntryId }) }),
  forkSession: (conversationId: string, entryId: string, title?: string) =>
    request<Conversation>(`/conversations/${conversationId}/fork`, { method: 'POST', body: JSON.stringify({ entry_id: entryId, title }) }),
  cloneSession: (conversationId: string, title?: string) =>
    request<Conversation>(`/conversations/${conversationId}/clone`, { method: 'POST', body: JSON.stringify({ title }) }),
  labelSessionEntry: (conversationId: string, entryId: string, label: string, expectedLeafEntryId: string) =>
    request<SessionEntry>(`/conversations/${conversationId}/entries/${entryId}/label`, { method: 'POST', body: JSON.stringify({ label, expected_leaf_entry_id: expectedLeafEntryId }) }),
  conversationQueue: (conversationId: string, status: ConversationQueueItem['status'] = 'pending') => request<{ items: ConversationQueueItem[] }>(`/conversations/${conversationId}/queue?status=${status}`),
  steerConversation: (conversationId: string, queueItemId: string) => request<{ item: ConversationQueueItem }>(`/conversations/${conversationId}/steer`, { method: 'POST', body: JSON.stringify({ queue_item_id: queueItemId }) }),
  cancelConversationQueueItem: (conversationId: string, queueItemId: string) => request<{ item: ConversationQueueItem }>(`/conversations/${conversationId}/queue/${queueItemId}`, { method: 'DELETE' }),
  projectTurn: (projectId: string, input: ProjectTurnInput) =>
		request<ProjectTurnResult>(`/projects/${projectId}/turns`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(input) }),
  importTranscript: (projectId: string, input: { source_format: 'oktapod-browser-export-v1' | 'pi-jsonl-preview-v1'; conversations: Array<{ title: string; messages: Array<{ role: ConversationMessage['role']; content: string; created_at: string }> }> }) =>
    request<{ project_id: string; conversation_ids: string[]; message_count: number }>(`/projects/${projectId}/transcript-import`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(input) }),
	streamProjectTurn: (projectId: string, input: ProjectTurnInput, handlers: StreamProjectTurnHandlers) =>
		streamProjectTurn(`/projects/${projectId}/turns/stream`, input, handlers),
  appendConversationMessage: (conversationId: string, input: { content: string; acceptance?: string; mode?: TaskMode; visibility?: Visibility; permission_mode?: PermissionMode | 'default'; scene?: Scene; model?: string; fallback_model?: string; max_model_calls?: number; max_tokens?: number; agent_id?: string; team_definition_id?: string; team_roles?: { role: string; name: string }[]; context_asset_ids?: string[]; context_memory_ids?: string[]; skill_id?: string; skill_args?: string }) =>
    request<Task>(`/conversations/${conversationId}/messages`, { method: 'POST', headers: { 'Idempotency-Key': crypto.randomUUID() }, body: JSON.stringify(input) }),
  projectActivity: (projectId: string, before = '') => request<{ items: ProjectActivity[] }>(`/projects/${projectId}/activity${before ? `?before=${encodeURIComponent(before)}` : ''}`),
  planItems: (projectId: string) => request<{ items: PlanItem[] }>(`/projects/${projectId}/plan-items`),
  createPlanItem: (projectId: string, input: { title: string; description?: string; visibility: Visibility }) =>
    request<PlanItem>(`/projects/${projectId}/plan-items`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify(input),
    }),
  movePlanItem: (planItemId: string, status: PlanStatus) =>
    request<PlanItem>(`/plan-items/${planItemId}/status`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify({ status }),
    }),
  assets: (projectId: string, options: { parentId?: string; query?: string; kind?: 'file' | 'folder' | '' } = {}) => {
    const params = new URLSearchParams()
    if (options.parentId) params.set('parent_id', options.parentId)
    if (options.query) params.set('q', options.query)
    if (options.kind) params.set('kind', options.kind)
    const query = params.toString()
    return request<AssetCollection>(`/projects/${projectId}/assets${query ? `?${query}` : ''}`)
  },
  createAssetFolder: (projectId: string, input: { name: string; parent_id?: string; visibility: Visibility }) =>
    request<Asset>(`/projects/${projectId}/asset-folders`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify(input),
    }),
  uploadAsset: (projectId: string, file: File, visibility: Visibility, parentId?: string) => {
    const body = new FormData()
    body.append('visibility', visibility)
    if (parentId) body.append('parent_id', parentId)
    body.append('file', file, file.name)
    return request<Asset>(`/projects/${projectId}/assets`, { method: 'POST', body })
  },
  assetContentUrl: (assetId: string) => `/api/v1/assets/${assetId}/content`,
  assetPreviewUrl: (assetId: string) => `/api/v1/assets/${assetId}/preview`,
  artifactContentUrl: (artifactId: string) => `/api/v1/artifacts/${artifactId}/content`,
  artifactPreviewUrl: (artifactId: string) => `/api/v1/artifacts/${artifactId}/preview`,
  fetchArtifactContent: async (artifactId: string): Promise<string> => {
    const response = await fetch(`/api/v1/artifacts/${artifactId}/preview`, { credentials: 'same-origin' })
    if (!response.ok) throw new Error(`Failed to load artifact: ${response.status}`)
    return response.text()
  },
  publishArtifact: (artifactId: string, input: { name: string; parent_id?: string; visibility: Visibility }) =>
    request<Asset>(`/artifacts/${artifactId}/publish`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify(input),
    }),
  createTask: (
    projectId: string,
    input: { title?: string; goal: string; acceptance?: string; mode: TaskMode; visibility: Visibility; permission_mode?: PermissionMode | 'default'; scene?: Scene; model?: string; fallback_model?: string; max_model_calls?: number; max_tokens?: number; agent_id?: string; team_definition_id?: string; team_roles?: { role: string; name: string }[]; context_asset_ids?: string[]; context_memory_ids?: string[]; skill_id?: string; skill_args?: string },
  ) =>
    request<Task>(`/projects/${projectId}/tasks`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify(input),
    }),
  task: (taskId: string) => request<Task>(`/tasks/${taskId}`),
  controlTask: (taskId: string, command: 'pause' | 'resume' | 'cancel' | 'retry') =>
    request<Task>(`/tasks/${taskId}/commands`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify({ command }),
    }),
  forkTask: (taskId: string) => request<Task>(`/tasks/${taskId}/fork`, {
    method: 'POST',
    headers: { 'Idempotency-Key': crypto.randomUUID() },
  }),
  acceptTaskOutcome: (taskId: string, note = '') =>
    request<{ id: string; task_id: string; run_id: string; accepted_by: string; note: string; created_at: string }>(`/tasks/${taskId}/acceptance`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify({ note }),
    }),
  runEvents: (runId: string, after = 0) => request<{ items: RunEvent[]; next_cursor: number }>(`/runs/${runId}/events?after=${after}`),
  runPlan: (runId: string) => request<RunPlanRevision>(`/runs/${runId}/plan`),
  humanActions: (options: { status?: HumanAction['status'] | 'all'; conversationId?: string; runId?: string } = {}) => {
    const params = new URLSearchParams()
    params.set('status', options.status ?? 'waiting_user')
    if (options.conversationId) params.set('conversation_id', options.conversationId)
    if (options.runId) params.set('run_id', options.runId)
    return request<{ items: HumanAction[] }>(`/human-actions?${params.toString()}`)
  },
  decideHumanAction: (actionId: string, input: { decision: 'approved' | 'rejected' | 'answered' | 'confirm_applied' | 'confirm_not_applied'; version: number; scope?: ApprovalScope; answer?: unknown; note?: string; confirmed?: boolean }) =>
    request<HumanAction>(`/human-actions/${actionId}/decision`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify(input),
    }),
  agentTeamRoom: (runId: string) => request<AgentTeamRoom>(`/runs/${runId}/agent-team`),
  runUsage: (runId: string) => request<RunUsage>(`/runs/${runId}/usage`),
  updateRunBudget: (runId: string, input: { max_model_calls: number; max_tokens: number; approve: boolean }) =>
    request<RunUsage>(`/runs/${runId}/budget`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(input) }),
  createAdvisorReport: (runId: string, approve = false, triggerReason = '用户请求') =>
    request<AdvisorReport>(`/runs/${runId}/advisor`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify({ approve, trigger_reason: triggerReason }),
    }),
  runArtifacts: (runId: string) => request<{ items: Artifact[] }>(`/runs/${runId}/artifacts`),
  runChanges: (runId: string, baseline: RunChanges['baseline']) => request<RunChanges>(`/runs/${runId}/changes?baseline=${baseline}`),
  approvals: (status: 'pending' | 'approved' | 'rejected' | 'all' = 'pending') => request<{ items: Approval[] }>(`/approvals?status=${status}`),
  sideEffectReceipts: (status: 'started' | 'succeeded' | 'failed' | 'unknown' | 'all' = 'unknown') => request<{ items: SideEffectReceipt[] }>(`/side-effect-receipts?status=${status}`),
  inbox: () => request<{ items: InboxItem[]; history: InboxHistoryItem[] }>('/inbox'),
  decideApproval: (approvalId: string, decision: 'approved' | 'rejected', note = '', scope: ApprovalScope = 'once', confirmed = false) =>
    request<Approval>(`/approvals/${approvalId}/decision`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify({ decision, scope, note, confirmed }),
    }),
  projectApprovalRules: (projectId: string) => request<{ items: ApprovalRule[] }>(`/projects/${projectId}/approval-rules`),
  revokeProjectApprovalRule: (projectId: string, ruleId: string) =>
    request<void>(`/projects/${projectId}/approval-rules/${ruleId}`, { method: 'DELETE' }),
  resolveSideEffect: (receiptId: string, decision: 'confirm_applied' | 'confirm_not_applied' | 'abandon', note = '') =>
    request<SideEffectReceipt>(`/side-effect-receipts/${receiptId}/resolution`, {
      method: 'POST',
      headers: { 'Idempotency-Key': crypto.randomUUID() },
      body: JSON.stringify({ decision, note }),
    }),
}
