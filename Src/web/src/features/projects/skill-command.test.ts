import { describe, expect, it } from 'vitest'
import { isCompactCommand, parseSkillInvocation, skillCommandName } from './skill-command'

const skills = [
  { id: 'skill-release', kind: 'skill' as const, name: 'Release Check', status: 'enabled' as const },
  { id: 'skill-disabled', kind: 'skill' as const, name: 'Disabled Skill', status: 'disabled' as const },
]

describe('Skill slash commands', () => {
	it('recognizes the built-in compact command with optional instructions', () => {
		expect(isCompactCommand('/compact')).toBe(true)
		expect(isCompactCommand('/compact keep file paths')).toBe(true)
		expect(isCompactCommand('/compaction')).toBe(false)
	})
  it('maps a normalized command to a Skill id and preserves arguments', () => {
    expect(skillCommandName(' Release Check ')).toBe('release-check')
    expect(parseSkillInvocation('/release-check verify production', skills)).toEqual({
      skillId: 'skill-release',
      skillName: 'Release Check',
      skillArgs: 'verify production',
    })
  })

  it('fails closed for unknown, disabled, or ambiguous commands', () => {
    expect(parseSkillInvocation('/unknown', skills)).toBeNull()
    expect(parseSkillInvocation('/disabled-skill', skills)).toBeNull()
    expect(parseSkillInvocation('/release-check', [...skills, { id: 'duplicate', kind: 'skill', name: 'Release Check', status: 'enabled' }])).toBeNull()
  })
})
