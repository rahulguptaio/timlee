import { afterEach, describe, expect, it } from 'vitest';

import type { TimleeTable } from './table';
import { $, cleanup, countScrollWidthReads, mount, moveAround, record, settle } from './test-utils';

afterEach(cleanup);

const MARKUP = `
  <timlee-table label="API keys" sortable hoverable>
    <table>
      <caption>Keys issued this month</caption>
      <thead>
        <tr>
          <th scope="col" data-sort="text">Name</th>
          <th scope="col" data-sort="number">Requests</th>
          <th scope="col">Created</th>
        </tr>
      </thead>
      <tbody>
        <tr><td>staging</td><td data-sort-value="90">90</td><td>2 days ago</td></tr>
        <tr><td>prod</td><td data-sort-value="1200">1.2k</td><td>8 days ago</td></tr>
        <tr><td>Analytics</td><td data-sort-value="300">300</td><td>1 day ago</td></tr>
      </tbody>
    </table>
  </timlee-table>
`;

function names(container: ParentNode): string[] {
  return Array.from(container.querySelectorAll('tbody tr td:first-child')).map(
    (cell) => cell.textContent ?? '',
  );
}

describe('<timlee-table>', () => {
  it('keeps the native table structure untouched', () => {
    const el = $<TimleeTable>(mount(MARKUP), 'timlee-table');
    expect(el.table?.tagName).toBe('TABLE');
    expect(el.querySelector('caption')?.textContent).toBe('Keys issued this month');
    expect(el.querySelectorAll('th[scope="col"]')).toHaveLength(3);
  });

  it('turns only the marked headers into sort buttons', () => {
    const container = mount(MARKUP);
    const buttons = container.querySelectorAll('.timlee-table__sort');
    expect(buttons).toHaveLength(2);
    expect(buttons[0]?.textContent).toBe('Name');
    expect(container.querySelectorAll('th[aria-sort="none"]')).toHaveLength(2);
  });

  it('sorts text with numeric collation and toggles direction', () => {
    const container = mount(MARKUP);
    const header = $<HTMLTableCellElement>(container, 'th[data-sort="text"]');
    const button = $<HTMLButtonElement>(header, '.timlee-table__sort');

    button.click();
    expect(header.getAttribute('aria-sort')).toBe('ascending');
    expect(names(container)).toEqual(['Analytics', 'prod', 'staging']);

    button.click();
    expect(header.getAttribute('aria-sort')).toBe('descending');
    expect(names(container)).toEqual(['staging', 'prod', 'Analytics']);
  });

  it('sorts numbers by data-sort-value, not by the displayed text', () => {
    const container = mount(MARKUP);
    const header = container.querySelectorAll<HTMLTableCellElement>('th[data-sort]')[1];
    if (!header) throw new Error('fixture');

    $<HTMLButtonElement>(header, '.timlee-table__sort').click();
    // "1.2k" would sort before "300" as text; the sort value says otherwise.
    expect(names(container)).toEqual(['staging', 'Analytics', 'prod']);
  });

  it('clears aria-sort on the other columns', () => {
    const container = mount(MARKUP);
    const headers = Array.from(container.querySelectorAll<HTMLTableCellElement>('th[data-sort]'));
    headers[0]?.querySelector<HTMLButtonElement>('.timlee-table__sort')?.click();
    headers[1]?.querySelector<HTMLButtonElement>('.timlee-table__sort')?.click();

    expect(headers[0]?.getAttribute('aria-sort')).toBe('none');
    expect(headers[1]?.getAttribute('aria-sort')).toBe('ascending');
  });

  it('announces the sort', () => {
    const container = mount(MARKUP);
    const el = $<TimleeTable>(container, 'timlee-table');
    const events = record<CustomEvent<{ column: number; direction: string }>>(el, 'timlee-sort');

    $<HTMLButtonElement>(container, '.timlee-table__sort').click();
    expect(events()[0]?.detail).toMatchObject({ column: 0, direction: 'ascending' });
  });

  it('is not a tab stop while it fits', async () => {
    const el = $<TimleeTable>(mount(MARKUP), 'timlee-table');
    el.style.inlineSize = '900px';
    await settle();
    expect(el.hasAttribute('tabindex')).toBe(false);
    expect(el.hasAttribute('role')).toBe(false);
  });

  it('becomes a focusable labelled region once it scrolls (WCAG 2.1.1)', async () => {
    const el = $<TimleeTable>(mount(MARKUP), 'timlee-table');
    el.style.inlineSize = '120px';
    await settle();

    expect(el.getAttribute('role')).toBe('region');
    expect(el.getAttribute('tabindex')).toBe('0');
    expect(el.getAttribute('aria-label')).toBe('API keys');
  });

  it('numbers scrolling tables that share a label, so each region is distinct', async () => {
    const narrow = (markup: string) =>
      markup.replace('<timlee-table', '<timlee-table style="inline-size:120px"');
    const container = mount(
      narrow(MARKUP) +
        narrow(MARKUP.replace('label="API keys" ', '')) +
        narrow(MARKUP) +
        narrow(MARKUP.replace('label="API keys" ', '')),
    );
    await settle();

    const names = Array.from(container.querySelectorAll('timlee-table'), (table) =>
      table.getAttribute('aria-label'),
    );
    // axe landmark-unique: a landmark list shows each region by name alone.
    expect(names).toEqual([
      'API keys 1',
      '\u2068Table 1\u2069, scrollable',
      'API keys 2',
      '\u2068Table 2\u2069, scrollable',
    ]);
  });

  it('is a keyboard-reachable region in off-screen content-visibility content', async () => {
    // Skipped by the browser, the table is never laid out or observed until
    // the reader nears it, but an audit that forces its layout sees it scroll.
    const container = mount(`
      <div style="block-size:300vh"></div>
      <section style="content-visibility:auto;contain-intrinsic-size:auto 20rem">
        ${MARKUP.replace('<timlee-table', '<timlee-table style="inline-size:120px"')}
      </section>
    `);
    const el = $<TimleeTable>(container, 'timlee-table');
    await settle();
    await settle();

    expect(el.getAttribute('role')).toBe('region');
    expect(el.getAttribute('tabindex')).toBe('0');
    expect(el.getAttribute('aria-label')).toBe('API keys');
  });

  it('measures its overflow after layout, never while it upgrades', async () => {
    const reads = countScrollWidthReads();
    try {
      const el = $<TimleeTable>(mount(MARKUP), 'timlee-table');
      // A read here would force the page's layout in the upgrade's task.
      expect(reads.count).toBe(0);
      el.style.inlineSize = '120px';
      await settle();
      expect(reads.count).toBeGreaterThan(0);
      expect(el.getAttribute('role')).toBe('region');
    } finally {
      reads.restore();
    }
  });

  it('sorts exactly once per click after the table is moved and put back', () => {
    const container = mount(MARKUP);
    const el = $<TimleeTable>(container, 'timlee-table');
    const events = record(el, 'timlee-sort');
    moveAround(el);

    $<HTMLButtonElement>(container, '.timlee-table__sort').click();

    expect(events()).toHaveLength(1);
    expect($(container, 'th[data-sort]').getAttribute('aria-sort')).toBe('ascending');
  });

  it('leaves a non-sortable table entirely alone', () => {
    const container = mount(MARKUP.replace(' sortable', ''));
    expect(container.querySelector('.timlee-table__sort')).toBeNull();
    expect(container.querySelector('th[aria-sort]')).toBeNull();
  });

  it('aligns a column by class, since the CSP refuses style and align is obsolete', () => {
    const container = mount(`
      <timlee-table label="Usage">
        <table>
          <thead><tr>
            <th scope="col" class="align-end">Requests</th>
            <th scope="col" class="align-center">Status</th>
            <th scope="col" class="align-start">Model</th>
          </tr></thead>
          <tbody><tr>
            <td class="align-end">4,812</td>
            <td class="align-center">Active</td>
            <td class="align-start">gpt-4o-mini</td>
          </tr></tbody>
        </table>
      </timlee-table>
    `);
    for (const [cls, align] of [
      ['align-end', 'end'],
      ['align-center', 'center'],
      ['align-start', 'start'],
    ]) {
      for (const cell of container.querySelectorAll(`.${cls}`)) {
        expect(getComputedStyle(cell).textAlign, `${cell.tagName}.${cls}`).toBe(align);
      }
    }
  });
});
