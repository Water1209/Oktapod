import type { Artifact } from '../../shared/api/client'

// A Run keeps an immutable Artifact for every successful file edit so history
// remains auditable. User-facing file lists represent logical deliverables,
// therefore they show only the newest version of each workspace path.
export function latestDeliverableArtifacts(artifacts: Artifact[]): Artifact[] {
  const latest = new Map<string, Artifact>()
  for (const artifact of artifacts.filter((item) => item.kind === 'deliverable')) {
    const key = artifact.name.trim().toLocaleLowerCase('en-US')
    const current = latest.get(key)
    if (!current || artifact.created_at > current.created_at || (artifact.created_at === current.created_at && artifact.id > current.id)) {
      latest.set(key, artifact)
    }
  }
  return [...latest.values()].sort((left, right) => left.created_at.localeCompare(right.created_at) || left.id.localeCompare(right.id))
}
