/**
 * app.ts — the script entry point.
 *
 * Loaded as a module (the bundle is ESM), so execution is deferred and the
 * parser is never blocked. Anything that must run before first paint belongs
 * in the inline head script instead.
 */

import { loadComponents } from '../components/loader';
import { landFragments } from './ts/fragments';
import { enhanceMenu, syncExpanded } from './ts/nav';
import { syncTabsToUrl } from './ts/tabs';
import { enhanceThemeToggles } from './ts/theme';

function onReady(run: () => void): void {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', run, { once: true });
  } else {
    run();
  }
}

onReady(() => {
  enhanceThemeToggles();
  // Only the components this page's markup needs, each a chunk of its own.
  void loadComponents();
  if (document.body.classList.contains('settings-app')) {
    void import('./ts/settings').then(({ enhanceSettings }) => enhanceSettings());
  }
  const workspace = document.querySelector<HTMLElement>('[data-workspace]');
  if (workspace) {
    void import('./ts/workspace').then(({ enhanceWorkspace }) => enhanceWorkspace(workspace));
  }

  syncExpanded(document);
  const menu = document.getElementById('site-menu');
  if (menu) enhanceMenu(menu);
  syncTabsToUrl(document);
  landFragments();
  // __DEV__ is a build-time constant, so this whole branch is eliminated from
  // the production bundle rather than shipped and skipped.
  if (__DEV__) {
    console.info('[timlee] assets served from the Go pipeline');
  }
});
