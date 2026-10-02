// Dark (the default, as on the host's server dashboard), light, or following the system. The choice is kept in
// localStorage and applied as the `dark` class on <html> before the first render (main.tsx); there is no inline script
// to do it earlier, because the CSP forbids one.

export type Theme = 'light' | 'dark' | 'system'

const key = 'mtxui-theme'
const media = () => window.matchMedia('(prefers-color-scheme: dark)')

export function storedTheme(): Theme {
  try {
    const v = localStorage.getItem(key)
    if (v === 'light' || v === 'dark' || v === 'system') return v
  } catch {
    // storage blocked: the default
  }
  return 'dark'
}

function apply(theme: Theme) {
  const dark = theme === 'dark' || (theme === 'system' && media().matches)
  document.documentElement.classList.toggle('dark', dark)
  document.documentElement.style.colorScheme = dark ? 'dark' : 'light'
}

export function setTheme(theme: Theme) {
  try {
    localStorage.setItem(key, theme)
  } catch {
    // storage blocked: the choice lasts until reload
  }
  apply(theme)
}

/** Applies the stored theme and follows system changes while the choice is "system". */
export function initTheme() {
  apply(storedTheme())
  media().addEventListener('change', () => {
    if (storedTheme() === 'system') apply('system')
  })
}
