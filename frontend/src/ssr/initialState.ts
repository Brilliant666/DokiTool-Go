import type { AppLocale } from '../types/api'

export interface HomeInitialState {
  prerendered: boolean
  locale: AppLocale
  isDark: boolean
  inputUrl: string
}

export const homeInitialStateKey = 'dokitool-home-initial-state'

export function readBrowserPreferences(): Pick<HomeInitialState, 'locale' | 'isDark'> {
  let savedLanguage: string | null = null
  let savedTheme: string | null = null
  try {
    savedLanguage = localStorage.getItem('lang')
    savedTheme = localStorage.getItem('theme')
  } catch { /* Storage can be disabled without preventing the page from opening. */ }
  const language = typeof navigator === 'undefined' ? 'zh-CN' : navigator.language
  const locale = savedLanguage === 'en' || savedLanguage === 'zh-CN'
    ? savedLanguage
    : language.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
  const isDark = savedTheme === 'dark' || (savedTheme !== 'light'
    && typeof window !== 'undefined' && Boolean(window.matchMedia?.('(prefers-color-scheme: dark)').matches))
  return { locale, isDark }
}

export function persistPreference(key: 'lang' | 'theme', value: string): void {
  try { localStorage.setItem(key, value) } catch { /* The preference still applies in this tab. */ }
}

const IN_APP_BROWSER_RE =
  /\bMicroMessenger\b|\bQQ\/|(?:^|[\s/])aweme(?:[/_.\s]|$)|\bBytedanceWebview\b|\bWeibo\b|__weibo__/i

export function isInAppBrowser(
  ua = typeof navigator === 'undefined' ? '' : navigator.userAgent,
): boolean {
  return Boolean(ua) && IN_APP_BROWSER_RE.test(ua)
}

export function getClientInitialState(root: HTMLElement): HomeInitialState {
  const prerendered = root.dataset.prerendered === 'true'
  // Read the live property, not the value attribute: users may already have typed
  // while the JavaScript was downloading. Vue starts with the same live value.
  const inputUrl = root.querySelector<HTMLInputElement>('[data-initial-url]')?.value ?? ''
  return {
    prerendered,
    ...(prerendered ? { locale: 'zh-CN' as const, isDark: false } : readBrowserPreferences()),
    inputUrl,
  }
}
