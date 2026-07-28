import { spawnSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, resolve } from 'node:path'

const repositoryRoot = resolve(import.meta.dirname, '../..')
const output = resolve(repositoryRoot, 'Build', process.platform === 'win32' ? 'oktapod-e2e.exe' : 'oktapod-e2e')
const goCache = resolve(tmpdir(), 'oktapod-go-build')
mkdirSync(dirname(output), { recursive: true })
mkdirSync(goCache, { recursive: true })
const result = spawnSync('go', ['build', '-o', output, './Src/cmd/oktapod'], {
  cwd: repositoryRoot,
  env: { ...process.env, GOCACHE: goCache },
  encoding: 'utf8',
  stdio: 'pipe',
  shell: false,
})
if (result.error) throw result.error
if (result.status !== 0) {
  process.stdout.write(result.stdout ?? '')
  process.stderr.write(result.stderr ?? '')
  process.exit(result.status ?? 1)
}
