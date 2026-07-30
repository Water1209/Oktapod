const baseURL = process.env.OKTAPOD_SMOKE_URL || 'http://127.0.0.1:8787/api/v1'

const health = await fetch(`${baseURL}/health`)
if (!health.ok) throw new Error(`health failed: ${health.status}`)

const suffix = Date.now().toString(36)
const register = await fetch(`${baseURL}/auth/register`, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({
    username: `smoke-${suffix}`,
    display_name: 'Docker Smoke',
    password: `Smoke-${suffix}-Pass!`,
  }),
})
if (register.status !== 201) throw new Error(`registration failed: ${register.status} ${await register.text()}`)

const setCookies = register.headers.getSetCookie()
const cookie = setCookies.map((value) => value.split(';', 1)[0]).join('; ')
const csrfCookie = setCookies
  .map((value) => value.split(';', 1)[0])
  .find((value) => value.startsWith('oktapod_csrf='))
const csrf = csrfCookie?.slice('oktapod_csrf='.length)
if (!cookie || !csrf) throw new Error('registration did not return session and CSRF cookies')

const bootstrap = await fetch(`${baseURL}/bootstrap`, { headers: { Cookie: cookie } })
if (!bootstrap.ok) throw new Error(`authenticated bootstrap failed: ${bootstrap.status}`)

const expectedVersion = process.env.OKTAPOD_EXPECT_VERSION || '1.0.0'
const diagnostics = await fetch(`${baseURL}/diagnostics`, { headers: { Cookie: cookie } })
if (!diagnostics.ok) throw new Error(`diagnostics failed: ${diagnostics.status}`)
const runtime = await diagnostics.json()
if (runtime.version !== expectedVersion) {
  throw new Error(`runtime version mismatch: ${runtime.version || '<empty>'}, expected ${expectedVersion}`)
}

const createProject = await fetch(`${baseURL}/projects`, {
  method: 'POST',
  headers: {
    'Content-Type': 'application/json',
    'Idempotency-Key': `docker-smoke-${suffix}`,
    'X-CSRF-Token': csrf,
    Cookie: cookie,
  },
  body: JSON.stringify({ name: 'Docker Smoke Project', kind: 'personal', scene: 'code' }),
})
if (createProject.status !== 201) throw new Error(`project creation failed: ${createProject.status} ${await createProject.text()}`)
const project = await createProject.json()
if (!project.id) throw new Error('created project has no id')

process.stdout.write(`Oktapod ${runtime.version} smoke passed for project ${project.id}\n`)
