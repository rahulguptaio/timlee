/**
 * <timlee-table> — a scroll container and sort affordance around a real
 * `<table>`.
 *
 *   <timlee-table label="API keys" sortable>
 *     <table>
 *       <thead><tr><th scope="col" data-sort="text">Name</th>…</tr></thead>
 *       <tbody>…</tbody>
 *     </table>
 *   </timlee-table>
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM. A `<table>` is already the accessible structure; wrapping
 *    it in a shadow root would separate `<caption>`, `scope` and `headers`
 *    references from the page and break nothing but gain nothing.
 *  - The host *is* the scroll container, in CSS, so a wide table scrolls
 *    before any script runs. Script only adds `tabindex="0"` and a labelled
 *    `role="region"` — and only while the table actually overflows, because
 *    an unnecessary tab stop on every table is its own accessibility problem.
 *    A scrollable region that cannot be reached by keyboard fails WCAG 2.1.1.
 *    A table inside off-screen `content-visibility: auto` content cannot be
 *    measured until it renders, so it is offered as a region until then.
 *  - Client-side sorting is opt-in. The default assumption for a Go app is
 *    that `<th><a href="?sort=name">` sorts on the server, which keeps the
 *    order shareable and survives pagination; `sortable` is for tables that
 *    are entirely on the page already.
 */

import { t } from '../assets/ts/i18n';
import { distinctName, whenSkipped } from './a11y';
import { define, TimleeElement } from './base';

/** What a table is called; numbered by distinctName when it repeats. */
function tableName(el: Element): string {
  return el.getAttribute('label') ?? t('components.table.table');
}

type SortKind = 'text' | 'number' | 'date';
type SortDirection = 'ascending' | 'descending';

export class TimleeTable extends TimleeElement {
  #resizeObserver: ResizeObserver | null = null;
  /** True once the observer has reported this connection's layout. */
  #measured = false;
  /** True when the region's aria-label is the one this element gave it. */
  #named = false;
  /** The sort buttons hydrate() put in the headers, with the header each sorts by. */
  #sorters: Array<readonly [HTMLButtonElement, HTMLTableCellElement]> = [];

  get table(): HTMLTableElement | null {
    return this.querySelector<HTMLTableElement>(':scope > table');
  }

  protected override hydrate(): void {
    if (this.hasAttribute('sortable')) this.#makeSortable();
  }

  protected override connected(signal: AbortSignal): void {
    for (const [button, th] of this.#sorters) {
      button.addEventListener('click', () => this.sortBy(th), { signal });
    }
    // The observer's first callback comes after the next layout, when the
    // overflow is already known. Measuring here instead would force that
    // layout in the middle of the page's startup.
    if (typeof ResizeObserver !== 'function') {
      this.#syncScrollRegion();
      return;
    }
    this.#measured = false;
    this.#resizeObserver = new ResizeObserver(() => {
      this.#measured = true;
      this.#syncScrollRegion();
    });
    this.#resizeObserver.observe(this);
    const table = this.table;
    if (table) this.#resizeObserver.observe(table);
    signal.addEventListener('abort', () => this.#resizeObserver?.disconnect(), { once: true });
    whenSkipped(
      this,
      () => this.#measured,
      () => this.#markScrollRegion(true),
      signal,
    );
  }

  /**
   * A scrollable region has to be operable by keyboard, which means a tab
   * stop and a name — but only while it really scrolls. Both axes count:
   * `sticky` tables are usually height-capped, and a vertical scroller no
   * keyboard can reach fails WCAG 2.1.1 just as a horizontal one does.
   */
  #syncScrollRegion(): void {
    this.#markScrollRegion(
      this.scrollWidth > this.clientWidth + 1 || this.scrollHeight > this.clientHeight + 1,
    );
  }

  #markScrollRegion(scrolls: boolean): void {
    if (scrolls) {
      this.setAttribute('role', 'region');
      this.setAttribute('tabindex', '0');
      if (!this.hasAttribute('aria-label') && !this.hasAttribute('aria-labelledby')) {
        const name = distinctName(this, 'timlee-table', tableName);
        this.setAttribute(
          'aria-label',
          this.hasAttribute('label') ? name : t('components.table.scrollable', { label: name }),
        );
        this.#named = true;
      }
      return;
    }
    // As in code-block.ts: an offered region keeps its tab stop while it has
    // focus, and its own name goes with its role.
    if (this === document.activeElement) {
      this.addEventListener('blur', () => this.#syncScrollRegion(), { once: true });
      return;
    }
    this.removeAttribute('role');
    this.removeAttribute('tabindex');
    if (this.#named) {
      this.removeAttribute('aria-label');
      this.#named = false;
    }
  }

  #makeSortable(): void {
    const headers = this.querySelectorAll<HTMLTableCellElement>('thead th[data-sort]');
    for (const th of headers) {
      if (th.querySelector('.timlee-table__sort')) continue;
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'timlee-table__sort';
      button.append(...th.childNodes);
      th.append(button);
      th.setAttribute('aria-sort', th.getAttribute('aria-sort') ?? 'none');
      this.#sorters.push([button, th]);
    }
  }

  /** Sort the first tbody by a header cell, toggling direction. */
  sortBy(th: HTMLTableCellElement, direction?: SortDirection): void {
    const table = this.table;
    const body = table?.tBodies[0];
    if (!table || !body) return;

    const index = Array.from(th.parentElement?.children ?? []).indexOf(th);
    if (index < 0) return;

    const current = th.getAttribute('aria-sort');
    const next: SortDirection = direction ?? (current === 'ascending' ? 'descending' : 'ascending');
    const kind = (th.dataset.sort || 'text') as SortKind;

    const rows = Array.from(body.rows);
    const factor = next === 'ascending' ? 1 : -1;
    rows.sort((a, b) => factor * compare(cellValue(a, index), cellValue(b, index), kind));
    body.append(...rows);

    for (const other of this.querySelectorAll('thead th[aria-sort]')) {
      other.setAttribute('aria-sort', other === th ? next : 'none');
    }
    this.emit('timlee-sort', { column: index, direction: next, header: th });
  }
}

/** `data-sort-value` wins, so a cell can display "3 days ago" and sort by epoch. */
function cellValue(row: HTMLTableRowElement, index: number): string {
  const cell = row.cells[index];
  return (cell?.dataset.sortValue ?? cell?.textContent ?? '').trim();
}

function compare(a: string, b: string, kind: SortKind): number {
  if (kind === 'number') {
    return (
      (Number(a.replace(/[^\d.eE+-]/g, '')) || 0) - (Number(b.replace(/[^\d.eE+-]/g, '')) || 0)
    );
  }
  if (kind === 'date') {
    return (Date.parse(a) || 0) - (Date.parse(b) || 0);
  }
  // Numeric collation so "item 10" sorts after "item 9".
  return a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' });
}

define('timlee-table', TimleeTable);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-table': TimleeTable;
  }
}
