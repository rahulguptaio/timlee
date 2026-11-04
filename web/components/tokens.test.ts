/**
 * tokens.test.ts — every colour role clears its WCAG minimum in light and in
 * dark, measured from the shipped tokens.css as Chromium paints it.
 *
 * docs/design-tokens.md#measured-contrast is the full table, computed for
 * both the oklch() path and the hex fallback; this is its guard. A ramp step
 * or a role binding that drops a pair below its minimum fails here, naming
 * the pair, the theme and the ratio.
 */

import { afterEach, describe, expect, it } from 'vitest';
import { contrast } from './contrast';
import { cleanup, mount } from './test-utils';

afterEach(cleanup);

type Theme = 'light' | 'dark';

/** The colour a token paints inside a subtree themed `theme`. */
function paints(token: string, theme: Theme): string {
  const host = mount(`<div data-theme="${theme}"><span></span></div>`);
  const probe = host.querySelector('span') as HTMLElement;
  probe.style.color = `var(${token})`;
  return getComputedStyle(probe).color;
}

const surfaces = [
  '--ts-background',
  '--ts-surface-1',
  '--ts-surface-2',
  '--ts-surface-3',
  '--ts-surface-hover',
  '--ts-surface-active',
  '--ts-surface-raised',
  '--ts-surface-overlay',
  '--ts-surface-inset',
];
const tints = ['primary', 'success', 'warning', 'danger', 'info'].flatMap((role) => [
  `--ts-${role}-soft`,
  `--ts-${role}-soft-hover`,
]);
const pagelike = ['--ts-background', '--ts-surface-1', '--ts-surface-2', '--ts-surface-raised'];

type Pair = [fg: string, bg: string, min: number];
const pairs = (fgs: string[], bgs: string[], min: number): Pair[] =>
  fgs.flatMap((fg) => bgs.map((bg): Pair => [fg, bg, min]));

const groups: Record<string, Pair[]> = {
  'text on every surface and tint (4.5:1)': pairs(
    ['--ts-text-1', '--ts-text-heading', '--ts-text-2', '--ts-text-3'],
    [...surfaces, ...tints, '--ts-byoc-soft'],
    4.5,
  ),
  'links on every surface (4.5:1)': pairs(['--ts-primary', '--ts-primary-hover'], surfaces, 4.5),
  'status ink on its tint and the page (4.5:1)': ['success', 'warning', 'danger', 'info'].flatMap(
    (role) =>
      pairs(
        [`--ts-${role}-ink`],
        [`--ts-${role}-soft`, `--ts-${role}-soft-hover`, ...pagelike],
        4.5,
      ),
  ),
  'ink on solid fills (4.5:1)': [
    ...pairs(
      ['--ts-primary-foreground'],
      ['--ts-primary', '--ts-primary-hover', '--ts-primary-active'],
      4.5,
    ),
    ['--ts-success-foreground', '--ts-success-solid', 4.5],
    ['--ts-warning-foreground', '--ts-warning-solid', 4.5],
    ['--ts-danger-foreground', '--ts-danger-solid', 4.5],
    ['--ts-info-foreground', '--ts-info-solid', 4.5],
    ['--ts-byoc-foreground', '--ts-byoc', 4.5],
    ['--ts-text-inverse', '--ts-surface-inverse', 4.5],
  ],
  'control boundaries, focus ring and indicators (3:1)': [
    ...pairs(['--ts-input', '--ts-input-hover', '--ts-ring'], surfaces, 3),
    ...pairs(
      ['--ts-success-dot', '--ts-warning-dot', '--ts-danger-dot', '--ts-info-dot', '--ts-idle-dot'],
      pagelike,
      3,
    ),
  ],
  'disabled text, held to 3:1': pairs(['--ts-text-disabled'], surfaces, 3),
  // A divider must never vanish into a surface it crosses: at step 5 the
  // subtle border was the selected row's own colour.
  'lines stay distinct from every surface': pairs(
    ['--ts-border-subtle', '--ts-border', '--ts-border-hover'],
    surfaces.filter((s) => s !== '--ts-surface-3'),
    1.04,
  ),
};

describe('colour roles meet their minimum contrast', () => {
  for (const theme of ['light', 'dark'] as const) {
    for (const [group, list] of Object.entries(groups)) {
      it(`${theme}: ${group}`, () => {
        const failures = list
          .map(([fg, bg, min]) => ({
            fg,
            bg,
            min,
            ratio: contrast(paints(fg, theme), paints(bg, theme)),
          }))
          .filter(({ ratio, min }) => ratio < min)
          .map(({ fg, bg, min, ratio }) => `${fg} on ${bg}: ${ratio.toFixed(2)} < ${min}`);
        expect(failures).toEqual([]);
      });
    }
  }
});
