import { existsSync, appendFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { performance } from 'node:perf_hooks'

const scenarios = [
  ['auto_answer', '用一句话说明这个项目的用途，不调用任何工具。'],
  ['direct_task_answer', '作为任务直接给出三条简短建议，不调用工具，也不要先创建计划。'],
  ['single_safe_tool', '使用一次只读搜索取得一个权威事实，然后简短回答并附来源。'],
  ['parallel_safe_tools', '并行执行两个互不依赖的只读搜索，核对结果后回答。'],
  ['safe_exclusive_safe', '先做两个只读检索，再写入 outputs/perf-barrier.txt，最后只读回查验证文件。'],
  ['replace_plan_with_tool', '建立两个阶段的语义计划，并在同一响应中执行第一阶段的只读搜索。'],
  ['advance_plan_with_tool', '完成第一阶段后，在推进语义计划的同一响应中执行第二阶段的只读搜索。'],
  ['explicit_skill', '使用调用方预先选择的显式 Skill 完成一次受治理检查，不要调用 skill_invoke。'],
  ['long_context_round_10', '延续前一轮目标，保留当前计划、最新工具配对和交付证据，避免复述旧 reasoning。'],
  ['approval_gate', '执行一个必须经过人工审批的受治理动作，并在审批门禁处停止。'],
  ['tool_failure_recovery', '先触发一次可恢复的只读工具失败，再基于错误回执修复并完成。'],
  ['artifact_delivery', '生成 outputs/perf-artifact.md，验证后登记为 deliverable Artifact，再给出完成说明。'],
].map(([id, prompt]) => ({ id, prompt }))

const baseURL = (process.env.OKTAPOD_PERF_URL || 'http://127.0.0.1:8787/api/v1').replace(/\/$/, '')
const cookie = process.env.OKTAPOD_PERF_COOKIE || ''
const csrf = process.env.OKTAPOD_PERF_CSRF || ''
const projectID = process.env.OKTAPOD_PERF_PROJECT_ID || ''
const isolatedRoot = resolve(process.env.OKTAPOD_PERF_ISOLATED_DATA_ROOT || '.')
const marker = resolve(isolatedRoot, '.oktapod-perf-isolated')
const requestedIterations = Number(process.env.OKTAPOD_PERF_ITERATIONS || 0)
const selected = process.env.OKTAPOD_PERF_SCENARIO || ''
const outputPath = process.env.OKTAPOD_PERF_OUTPUT || ''
const explicitSkillID = process.env.OKTAPOD_PERF_SKILL_ID || ''

if (!cookie || !csrf || !projectID) throw new Error('OKTAPOD_PERF_COOKIE, OKTAPOD_PERF_CSRF and OKTAPOD_PERF_PROJECT_ID are required')
if (!process.env.OKTAPOD_PERF_ISOLATED_DATA_ROOT) throw new Error('OKTAPOD_PERF_ISOLATED_DATA_ROOT is required')
if (!Number.isInteger(requestedIterations) || requestedIterations < 1 || requestedIterations > 30) throw new Error('OKTAPOD_PERF_ITERATIONS must be an explicit integer from 1 to 30')
if (!existsSync(marker)) throw new Error(`isolated-data marker is missing: ${marker}`)
if (!selected) throw new Error(`OKTAPOD_PERF_SCENARIO is required; choose one of: ${scenarios.map((item) => item.id).join(', ')}`)
if (selected === 'explicit_skill' && !explicitSkillID) throw new Error('OKTAPOD_PERF_SKILL_ID is required for explicit_skill')
if (outputPath && !resolve(outputPath).startsWith(`${isolatedRoot}\\`) && resolve(outputPath) !== isolatedRoot) throw new Error('OKTAPOD_PERF_OUTPUT must stay inside the isolated data root')

const chosen = scenarios.filter((scenario) => scenario.id === selected)
if (chosen.length === 0) throw new Error(`unknown scenario: ${selected}`)

const headers = {
  'Content-Type': 'application/json',
  'X-CSRF-Token': csrf,
  Cookie: cookie,
}

async function request(path, init = {}) {
  const response = await fetch(`${baseURL}${path}`, { ...init, headers: { ...headers, ...(init.headers || {}) } })
  if (!response.ok) throw new Error(`${init.method || 'GET'} ${path}: ${response.status} ${await response.text()}`)
  return response.json()
}

async function waitForTask(taskID, timeoutMs = 60 * 60 * 1000) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const task = await request(`/tasks/${taskID}`)
    if (['succeeded', 'failed', 'blocked', 'canceled', 'waiting_approval', 'waiting_input'].includes(task.status)) return task
    if (Date.now() >= deadline) throw new Error(`task ${taskID} did not reach a terminal state`)
    await new Promise((resolveDelay) => setTimeout(resolveDelay, 250))
  }
}

function percentile(values, fraction) {
  if (values.length === 0) return 0
  const ordered = [...values].sort((left, right) => left - right)
  return ordered[Math.min(ordered.length - 1, Math.ceil(ordered.length * fraction) - 1)]
}

const records = []
for (const scenario of chosen) {
  for (let iteration = 1; iteration <= requestedIterations; iteration++) {
    const started = performance.now()
    const rounds = scenario.id === 'long_context_round_10' ? 10 : 1
    let conversationID = ''
    let turn
    let task
    let usage
    let events
    for (let round = 1; round <= rounds; round++) {
      const body = { content: round === 1 ? scenario.prompt : `${scenario.prompt}（第 ${round} 轮）`, mode: 'auto', visibility: 'private', permission_mode: 'ask' }
      if (conversationID) body.conversation_id = conversationID
      if (scenario.id === 'explicit_skill') body.skill_id = explicitSkillID
      turn = await request(`/projects/${projectID}/turns`, {
        method: 'POST',
        headers: { 'Idempotency-Key': `perf-${scenario.id}-${Date.now()}-${iteration}-${round}` },
        body: JSON.stringify(body),
      })
      if (!turn.task?.id || !turn.task?.run_id) throw new Error(`${scenario.id} did not create a durable auto Run`)
      conversationID = turn.conversation.id
      task = await waitForTask(turn.task.id)
      usage = await request(`/runs/${turn.task.run_id}/usage`)
      events = await request(`/runs/${turn.task.run_id}/events?after=0`)
      if (task.status !== 'succeeded') break
    }
    const record = {
      scenario: scenario.id,
      iteration,
      task_id: task.id,
      run_id: task.run_id,
      status: task.status,
      wall_ms: Math.round(performance.now() - started),
      model_calls: usage.model_calls,
      input_tokens: usage.input_tokens,
      output_tokens: usage.output_tokens,
      first_byte_ms: usage.first_byte_ms,
      first_safe_delta_ms: usage.first_safe_delta_ms,
      assembly_ms: usage.assembly_ms,
      request_bytes: usage.request_bytes,
      purposes: usage.model_call_purposes,
      durable_events: events.items.length,
    }
    records.push(record)
    const line = `${JSON.stringify(record)}\n`
    process.stdout.write(line)
    if (outputPath) appendFileSync(outputPath, line, 'utf8')
  }
}

const summary = chosen.map((scenario) => {
  const items = records.filter((record) => record.scenario === scenario.id)
  return {
    scenario: scenario.id,
    runs: items.length,
    succeeded: items.filter((record) => record.status === 'succeeded').length,
    wall_p50_ms: percentile(items.map((record) => record.wall_ms), 0.5),
    wall_p95_ms: percentile(items.map((record) => record.wall_ms), 0.95),
    model_calls_p50: percentile(items.map((record) => record.model_calls), 0.5),
    input_tokens_p50: percentile(items.map((record) => record.input_tokens), 0.5),
  }
})
process.stdout.write(`${JSON.stringify({ summary })}\n`)
