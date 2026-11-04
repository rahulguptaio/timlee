/**
 * Theme handling.
 *
 * The server writes data-theme only for a reader who chose a theme (the
 * timlee.theme cookie); without one, the stylesheet follows the system
 * through color-scheme. The toggle is a form that works without this module
 * (partials/theme-toggle.gohtml); with it, a choice applies without a reload.
 */

export type Theme = 'light' | 'dark';

const root = document.documentElement;
const darkSystem = matchMedia('(prefers-color-scheme: dark)');
const listeners = new Set<(theme: Theme) => void>();

const system = (): Theme => (darkSystem.matches ? 'dark' : 'light');

/** The theme on screen: the reader's choice, else the system's. */
export function currentTheme(): Theme {
  const chosen = root.dataset.theme;
  return chosen === 'light' || chosen === 'dark' ? chosen : system();
}

/** For surfaces drawn by script, which the stylesheet cannot re-theme. */
export function onThemeChange(listener: (theme: Theme) => void): void {
  listeners.add(listener);
}

function changed(): void {
  const theme = currentTheme();
  for (const listener of listeners) listener(theme);
}

let settleFrame = 0;

/** Swap every colour in one frame; see `.theme-switching` in base.css. */
function suppressTransitions(): void {
  root.classList.add('theme-switching');
  // Restarted on every call so a quick second toggle cannot lift the guard
  // before its own style change has been painted.
  cancelAnimationFrame(settleFrame);
  settleFrame = requestAnimationFrame(() => {
    settleFrame = requestAnimationFrame(() => root.classList.remove('theme-switching'));
  });
}

/**
 * Switch to the opposite of the theme on screen. Landing on the system's own
 * scheme forgets the choice instead of storing it, so the page follows the
 * system again — what the server does for the form posted without script.
 */
function toggle(): void {
  const next: Theme = currentTheme() === 'dark' ? 'light' : 'dark';
  const choice = next === system() ? '' : next;
  suppressTransitions();
  if (choice) root.dataset.theme = choice;
  else delete root.dataset.theme;
  // The cookie the server reads, so the next page arrives in this theme.
  // biome-ignore lint/suspicious/noDocumentCookie: the Cookie Store API is missing from WebKit before Safari 18.4, which the macOS app can run on.
  document.cookie = `timlee.theme=${choice}; Path=/; SameSite=Lax; Max-Age=${choice ? 31536000 : 0}${
    location.protocol === 'https:' ? '; Secure' : ''
  }`;
  for (const meta of document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]')) {
    const scheme = choice || (meta.media.includes('dark') ? 'dark' : 'light');
    meta.content = scheme === 'dark' ? '#0b1424' : '#ffffff';
  }
  changed();
}

/** Apply a toggle's choice in place rather than posting it and reloading. */
export function enhanceThemeToggles(): void {
  document.addEventListener('submit', (event) => {
    if (!(event.target as Element).matches('[data-theme-toggle]')) return;
    event.preventDefault();
    toggle();
  });
  // The stylesheet follows a system change by itself; script-drawn surfaces
  // have to be told.
  darkSystem.addEventListener('change', () => {
    if (!root.dataset.theme) changed();
  });
}
