export type ThemePreference = 'system' | 'light' | 'dark'
export type DensityPreference = 'comfortable' | 'compact'

export const presentationPreferenceStorageKey = 'oktapod.preferences.v1'

export type PresentationPreferences = {
  theme: ThemePreference
  density: DensityPreference
}

export const defaultPresentationPreferences: PresentationPreferences = {
  theme: 'system',
  density: 'compact',
}

export function readPresentationPreferences(): PresentationPreferences {
  try {
    const stored = JSON.parse(window.localStorage.getItem(presentationPreferenceStorageKey) || '{}') as Record<string, unknown>
    return {
      theme: stored.theme === 'light' || stored.theme === 'dark' ? stored.theme : 'system',
      density: stored.density === 'comfortable' ? 'comfortable' : 'compact',
    }
  } catch {
    return defaultPresentationPreferences
  }
}

export function applyPresentationPreferences(preferences: PresentationPreferences) {
  document.documentElement.dataset.theme = preferences.theme
  document.documentElement.dataset.density = preferences.density
}

export function applyStoredPresentationPreferences() {
  applyPresentationPreferences(readPresentationPreferences())
}
