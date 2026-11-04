/**
 * Vitest configuration for the component unit tests.
 *
 * Separate from the repository's root `vitest.config.ts`, which belongs to
 * Storybook: that config turns every *story* into a test (render + play +
 * axe). This one runs the *unit* tests that live beside each component —
 * attribute reflection, events, keyboard handling, the ARIA contract and the
 * server-rendered upgrade path. Two different questions, two projects.
 *
 * WHY BROWSER MODE RATHER THAN JSDOM / HAPPY-DOM
 * Everything under test here is a platform feature:
 *
 *   - custom element upgrade order and `connectedCallback` timing,
 *   - `ElementInternals` and form association (`setFormValue`, reset),
 *   - `<dialog>.showModal()`, `:modal`, the top layer and the focus trap,
 *   - the Popover API, `:popover-open`,
 *   - `:has()`, `:focus-visible`, computed styles from real cascade order,
 *   - `offsetParent` / `getClientRects()` based visibility.
 *
 * jsdom implements none of `showModal`'s focus behaviour, has no
 * `ElementInternals` form association and no layout at all, so a green jsdom
 * run would be evidence about jsdom. Real Chromium via Playwright is the only
 * environment where passing means the component works.
 */

import { playwright } from '@vitest/browser-playwright';
import { defineConfig } from 'vitest/config';

export default defineConfig({
  define: {
    // Mirrors the Define that cmd/timlee-build passes to esbuild, so the
    // dev-only branches compile the same way they do in production.
    __DEV__: 'false',
  },
  test: {
    name: 'components',
    dir: import.meta.dirname,
    include: ['**/*.test.ts'],
    testTimeout: 15_000,
    browser: {
      enabled: true,
      headless: true,
      provider: playwright(),
      instances: [{ browser: 'chromium' }],
      screenshotFailures: false,
    },
  },
});
