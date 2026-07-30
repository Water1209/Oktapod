import { describe, expect, it } from 'vitest'
import { parseUnifiedDiff } from './run-diff'

describe('parseUnifiedDiff', () => {
  it('groups files and hunks for selectable Changes actions', () => {
    const files = parseUnifiedDiff([
      'diff --git a/src/a.ts b/src/a.ts',
      '--- a/src/a.ts',
      '+++ b/src/a.ts',
      '@@ -1 +1 @@',
      '-old',
      '+new',
      'diff --git a/README.md b/README.md',
      '--- a/README.md',
      '+++ b/README.md',
      '@@ -2,0 +3 @@',
      '+release',
    ].join('\n'))

    expect(files.map((file) => file.path)).toEqual(['src/a.ts', 'README.md'])
    expect(files[0]?.hunks[0]).toEqual({ header: '@@ -1 +1 @@', lines: ['-old', '+new'] })
  })

  it('uses the old path for a deleted file', () => {
    const files = parseUnifiedDiff([
      'diff --git a/obsolete.txt b/obsolete.txt',
      '--- a/obsolete.txt',
      '+++ /dev/null',
      '@@ -1 +0,0 @@',
      '-obsolete',
    ].join('\n'))

    expect(files[0]?.path).toBe('obsolete.txt')
  })
})
