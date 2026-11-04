/**
 * forced-colors.test.ts — the components under a Windows contrast theme,
 * which WebView2 (the Windows desktop shell) honours.
 *
 * In forced-colors mode the browser repaints every author colour with a
 * system colour: backgrounds become Canvas and every border one colour. A
 * glyph cut out of a background vanishes, and a state shown only by a border
 * colour stops showing. Chromium's emulation applies the same forcing, so
 * these read the colours the reader would actually get.
 */

import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { cdp } from 'vitest/browser';

import { $, cleanup, mount } from './test-utils';

async function forcedColors(value: 'active' | 'none'): Promise<void> {
  await cdp().send('Emulation.setEmulatedMedia', {
    features: [{ name: 'forced-colors', value }],
  });
}

// Fixtures are mounted after the switch, so no colour transition is caught
// half-way: a colour mid-transition is not a system colour.
beforeAll(() => forcedColors('active'));
afterAll(() => forcedColors('none'));
afterEach(cleanup);

/** A system colour as this browser resolves it. */
function system(keyword: string): string {
  const probe = document.createElement('span');
  probe.style.color = keyword;
  document.body.append(probe);
  const value = getComputedStyle(probe).color;
  probe.remove();
  return value;
}

const background = (el: Element, pseudo?: string) => getComputedStyle(el, pseudo).backgroundColor;

describe('forced colours (Windows contrast themes)', () => {
  it('is what the browser is emulating', () => {
    expect(matchMedia('(forced-colors: active)').matches).toBe(true);
    expect(system('CanvasText')).not.toBe(system('Canvas'));
  });

  it('keeps the select chevron, the field\u2019s only arrow', () => {
    const select = $(
      mount(
        '<timlee-select><label for="env">Env</label><select id="env"></select></timlee-select>',
      ),
      'timlee-select',
    );
    expect(background(select, '::after')).toBe(system('CanvasText'));
  });

  it('keeps every alert tone glyph, the shape that says the tone without colour', () => {
    const container = mount(
      ['info', 'success', 'warning', 'danger']
        .map((tone) => `<timlee-alert tone="${tone}">Note</timlee-alert>`)
        .join(''),
    );
    for (const alert of container.querySelectorAll('timlee-alert')) {
      expect(background(alert, '::before'), alert.getAttribute('tone') ?? '').toBe(
        system('CanvasText'),
      );
    }
  });

  it('keeps the sort arrows, sorted and unsorted', () => {
    const container = mount(`
      <timlee-table sortable>
        <table>
          <thead><tr>
            <th scope="col" data-sort="text">Name</th>
            <th scope="col" data-sort="number">Requests</th>
          </tr></thead>
          <tbody><tr><td>prod</td><td>4</td></tr></tbody>
        </table>
      </timlee-table>
    `);
    $<HTMLButtonElement>(container, '.timlee-table__sort').click();

    for (const button of container.querySelectorAll('.timlee-table__sort')) {
      expect(background(button, '::after')).toBe(system('ButtonText'));
      expect(getComputedStyle(button, '::after').opacity).toBe('1');
    }
  });

  it('tells the selected tab from the others, in both orientations', () => {
    const tabs = (orientation: string) => `
      <timlee-tabs orientation="${orientation}">
        <div data-tablist aria-label="Key details">
          <a href="#${orientation}-usage">Usage</a>
          <a href="#${orientation}-limits">Limits</a>
        </div>
        <section id="${orientation}-usage" data-tabpanel><p>Usage</p></section>
        <section id="${orientation}-limits" data-tabpanel><p>Limits</p></section>
      </timlee-tabs>
    `;
    const container = mount(tabs('horizontal') + tabs('vertical'));

    const indicator = (tab: Element, side: 'borderBlockEndColor' | 'borderInlineEndColor') =>
      getComputedStyle(tab)[side];
    const [horizontal, vertical] = container.querySelectorAll('timlee-tabs');
    for (const [host, side] of [
      [horizontal, 'borderBlockEndColor'],
      [vertical, 'borderInlineEndColor'],
    ] as const) {
      const [selected, other] = host?.querySelectorAll('[role="tab"]') ?? [];
      if (!selected || !other) throw new Error('fixture');
      expect(selected.getAttribute('aria-selected')).toBe('true');
      expect(indicator(selected, side)).toBe(system('Highlight'));
      expect(indicator(other, side)).toBe(system('Canvas'));
    }
  });

  it('marks the current page the way the system marks a selection', () => {
    const container = mount(`
      <timlee-pagination>
        <nav aria-label="Pagination">
          <a href="?page=1">1</a><a href="?page=2" aria-current="page">2</a>
        </nav>
      </timlee-pagination>
    `);
    const current = $(container, '[aria-current="page"]');
    expect(background(current)).toBe(system('Highlight'));
    expect(getComputedStyle(current).color).toBe(system('HighlightText'));
    expect(background($(container, 'a:not([aria-current])'))).not.toBe(system('Highlight'));
  });

  it('outlines the progress track, which would otherwise not be drawn at all', () => {
    const progress = $(
      mount(
        '<timlee-progress><progress max="100" value="60" aria-label="Quota"></progress></timlee-progress>',
      ),
      'progress',
    );
    const style = getComputedStyle(progress);
    expect(style.borderTopStyle).toBe('solid');
    expect(style.borderTopColor).toBe(system('CanvasText'));
  });
});
