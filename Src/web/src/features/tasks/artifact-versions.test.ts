import { describe, expect, it } from 'vitest'
import type { Artifact } from '../../shared/api/client'
import { latestDeliverableArtifacts } from './artifact-versions'

function artifact(id: string, name: string, createdAt: string, sizeBytes: number, kind = 'deliverable'): Artifact {
  return {
    id,
    name,
    created_at: createdAt,
    size_bytes: sizeBytes,
    project_id: 'project-1',
    task_id: 'task-1',
    run_id: 'run-1',
    step_id: `step-${id}`,
    kind,
    media_type: 'text/markdown',
    content_hash: `sha256:${id}`,
    visibility: 'private',
    source_type: 'workspace',
  }
}

describe('latestDeliverableArtifacts', () => {
  it('shows one logical file while retaining the newest immutable artifact', () => {
    const result = latestDeliverableArtifacts([
      artifact('a', 'report.md', '2026-08-10T01:00:00Z', 100),
      artifact('b', 'notes.md', '2026-08-10T01:01:00Z', 80),
      artifact('c', 'REPORT.md', '2026-08-10T01:02:00Z', 300),
      artifact('d', 'tool-result.json', '2026-08-10T01:03:00Z', 500, 'tool_result'),
    ])

    expect(result.map((item) => [item.id, item.name, item.size_bytes])).toEqual([
      ['b', 'notes.md', 80],
      ['c', 'REPORT.md', 300],
    ])
  })
})
