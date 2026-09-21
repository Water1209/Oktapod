import type { HumanAction } from '../../shared/api/client'

export const approvalPending = (action: HumanAction) => action.status === 'waiting_user' || action.status === 'reviewing'

export const approvalBatchable = (action: HumanAction) => action.kind === 'tool_approval' && action.status === 'waiting_user' && action.request.approval_scope !== 'run_origin'

// Keep the reviewed cohort and its order stable while SSE refreshes status.
// Historical actions that were already finished on entry do not fill the panel.
export function reconcileApprovals(previous: HumanAction[], incoming: HumanAction[]) {
  const byId = new Map(incoming.map((item) => [item.id, item]))
  const known = new Set(previous.map((item) => item.id))
  return [
    ...previous.map((item) => byId.get(item.id) ?? { ...item, status: 'superseded' as const }),
    ...incoming.filter((item) => approvalPending(item) && !known.has(item.id)),
  ]
}
