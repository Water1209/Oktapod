import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'

// Only administrator UI scenarios use this deployment entry. Other accounts
// remain members so the suite continues to exercise ordinary-user behavior.
export function assignTestInstanceOwner(username: string) {
  const dataRoot = process.env.OKTAPOD_E2E_DATA_ROOT
  if (!dataRoot) throw new Error('Owner fixture requires the isolated E2E DataRoot')
  const binary = resolve(__dirname, '../../Build', process.platform === 'win32' ? 'oktapod-e2e.exe' : 'oktapod-e2e')
  execFileSync(binary, ['user', 'set-owner', '--username', username], {
    env: { ...process.env, OKTAPOD_CONFIG_FILE: resolve(dataRoot, 'config.yaml') },
    stdio: 'pipe',
    timeout: 30_000,
  })
}
