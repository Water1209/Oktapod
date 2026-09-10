import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { AuthenticationLifetime } from './app/authentication-lifetime'
import { applyStoredPresentationPreferences } from './shared/presentation-preferences'
import './styles.css'

applyStoredPresentationPreferences()

createRoot(document.getElementById('root')!).render(<StrictMode><AuthenticationLifetime /></StrictMode>)