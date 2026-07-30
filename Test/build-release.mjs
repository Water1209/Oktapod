import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { arch, platform } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'))
const version = String(manifest.version || '').trim()
if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(version)) {
  throw new Error(`package.json contains an invalid release version: ${version || '<empty>'}`)
}

const targetPlatform = platform() === 'win32' ? 'windows' : platform()
const targetArch = ({ x64: 'amd64', arm64: 'arm64' })[arch()] || arch()
const extension = targetPlatform === 'windows' ? '.exe' : ''
const artifactName = `oktapod-v${version}-${targetPlatform}-${targetArch}${extension}`
const output = join(root, 'Build', artifactName)

mkdirSync(join(root, 'Build'), { recursive: true })
if (process.env.npm_execpath) {
  execFileSync(process.execPath, [process.env.npm_execpath, 'build:web'], { cwd: root, stdio: 'inherit' })
} else {
  execFileSync('pnpm', ['build:web'], { cwd: root, stdio: 'inherit' })
}
execFileSync('go', [
  'build',
  '-trimpath',
  '-ldflags',
  `-s -w -X github.com/oktapod/oktapod/Src/internal/application.RuntimeVersion=${version}`,
  '-o',
  output,
  './Src/cmd/oktapod',
], { cwd: root, stdio: 'inherit' })

const reportedVersion = execFileSync(output, ['version'], { cwd: root, encoding: 'utf8' }).trim()
if (reportedVersion !== version) {
  throw new Error(`release binary reported ${reportedVersion || '<empty>'}, expected ${version}`)
}

const digest = createHash('sha256').update(readFileSync(output)).digest('hex')
const checksumPath = `${output}.sha256`
writeFileSync(checksumPath, `${digest}  ${artifactName}\n`, 'utf8')

process.stdout.write(`Release artifact: ${output}\n`)
process.stdout.write(`Version: ${reportedVersion}\n`)
process.stdout.write(`SHA256: ${digest}\n`)
