import { createContext, useContext } from 'react'
import type { Bootstrap } from './api/client'

export const BootstrapContext = createContext<Bootstrap | null>(null)

export function useBootstrap() {
  const bootstrap = useContext(BootstrapContext)
  if (!bootstrap) throw new Error('BootstrapContext is required')
  return bootstrap
}
