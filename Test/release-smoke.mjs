import { execFileSync, spawn } from 'node:child_process'
import { closeSync, mkdirSync, mkdtempSync, openSync, readFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { arch, platform } from 'node:os'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = dirname(dirname(fileURLToPath(import.meta.url)))
const manifest = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'))
const version = String(manifest.version)
const targetPlatform = platform() === 'win32' ? 'windows' : platform()
const targetArch = ({ x64: 'amd64', arm64: 'arm64' })[arch()] || arch()
const extension = targetPlatform === 'windows' ? '.exe' : ''
const binary = join(root, 'Build', `oktapod-v${version}-${targetPlatform}-${targetArch}${extension}`)

mkdirSync(join(root, 'Build'), { recursive: true })
const dataRoot = mkdtempSync(join(root, 'Build', 'rc-smoke-'))
const stdoutPath = join(dataRoot, 'stdout.log')
const stderrPath = join(dataRoot, 'stderr.log')
const stdout = openSync(stdoutPath, 'w')
const stderr = openSync(stderrPath, 'w')

const port = await new Promise((resolve, reject) => {
  const probe = createServer()
  probe.once('error', reject)
  probe.listen(0, '127.0.0.1', () => {
    const address = probe.address()
    const selected = typeof address === 'object' && address ? address.port : 0
    probe.close((error) => error ? reject(error) : resolve(selected))
  })
})
const baseURL = `http://127.0.0.1:${port}/api/v1`
const child = spawn(binary, ['serve'], {
  cwd: root,
  env: {
    ...process.env,
    OKTAPOD_LISTEN_ADDRESS: `127.0.0.1:${port}`,
    OKTAPOD_DATA_ROOT: dataRoot,
  },
  stdio: ['ignore', stdout, stderr],
  windowsHide: true,
})

try {
  let ready = false
  for (let attempt = 0; attempt < 60; attempt += 1) {
    if (child.exitCode !== null) throw new Error(`release process exited with code ${child.exitCode}`)
    try {
      const response = await fetch(`${baseURL}/health`)
      if (response.ok) {
        ready = true
        break
      }
    } catch {
      // The listener is expected to be unavailable during initial migrations.
    }
    await new Promise((resolve) => setTimeout(resolve, 500))
  }
  if (!ready) throw new Error('release process did not become healthy')

  execFileSync(process.execPath, [join(root, 'Test', 'docker-smoke.mjs')], {
    cwd: root,
    env: {
      ...process.env,
      OKTAPOD_SMOKE_URL: baseURL,
      OKTAPOD_EXPECT_VERSION: version,
    },
    stdio: 'inherit',
  })
  process.stdout.write(`Release smoke data: ${dataRoot}\n`)
} finally {
  child.kill()
  let stopped = child.exitCode !== null
  if (!stopped) {
    await Promise.race([
      new Promise((resolve) => child.once('exit', () => { stopped = true; resolve() })),
      new Promise((resolve) => setTimeout(resolve, 5_000)),
    ])
  }
  if (!stopped && child.exitCode === null) {
    child.kill('SIGKILL')
    await new Promise((resolve) => child.once('exit', resolve))
  }
  closeSync(stdout)
  closeSync(stderr)
}
