// Per-device UI preferences. They hold no secrets, so localStorage is enough.

export type ThemeChoice = 'dark' | 'light' | 'system'

type Prefs = {
  theme: ThemeChoice
  sounds: boolean
  reduceMotion: boolean
}

const KEY = 'arfl.prefs'
const DEFAULTS: Prefs = { theme: 'dark', sounds: false, reduceMotion: false }

function load(): Prefs {
  try {
    const raw = localStorage.getItem(KEY)
    return raw ? { ...DEFAULTS, ...JSON.parse(raw) } : { ...DEFAULTS }
  } catch {
    return { ...DEFAULTS }
  }
}

export const prefs = $state<Prefs>(load())

const darkQuery = window.matchMedia('(prefers-color-scheme: dark)')

function resolvedTheme(): 'dark' | 'light' {
  if (prefs.theme === 'system') return darkQuery.matches ? 'dark' : 'light'
  return prefs.theme
}

export function isLight(): boolean {
  return resolvedTheme() === 'light'
}

export function applyPrefs() {
  const root = document.documentElement
  root.dataset.theme = resolvedTheme()
  root.classList.toggle('reduce-motion', prefs.reduceMotion)
  try {
    localStorage.setItem(KEY, JSON.stringify(prefs))
  } catch {
    // Storage can be unavailable; the choice still applies for this session.
  }
}

darkQuery.addEventListener('change', () => {
  if (prefs.theme === 'system') applyPrefs()
})
