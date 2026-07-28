import type { Capability } from '../../shared/api/client'

export function skillCommandName(name: string) {
  return name.trim().replace(/\s+/g, '-').toLowerCase()
}

export function isCompactCommand(text: string) {
  return /^\/compact(?:\s|$)/i.test(text.trim())
}

export function parseSkillInvocation(text: string, skills: Array<Pick<Capability, 'id' | 'kind' | 'name' | 'status'>>) {
  const match = text.trim().match(/^\/([^\s]+)(?:\s+([\s\S]*))?$/)
  if (!match) return null
  const command = (match[1] ?? '').toLowerCase()
  const matches = skills.filter((skill) => skill.kind === 'skill' && skill.status === 'enabled' && skillCommandName(skill.name) === command)
  if (matches.length !== 1) return null
  const skill = matches[0]
  if (!skill) return null
  return { skillId: skill.id, skillName: skill.name, skillArgs: (match[2] ?? '').trim() }
}
