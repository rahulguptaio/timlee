/**
 * <timlee-tabs> — promotes a list of in-page links into an APG tab widget.
 *
 * WHAT THE SERVER WRITES (and what it means with no JavaScript):
 *   <timlee-tabs>
 *     <nav data-tablist aria-label="Key settings">
 *       <a href="#usage">Usage</a>
 *       <a href="#limits" aria-current="page">Limits</a>
 *     </nav>
 *     <section id="usage" data-tabpanel>…</section>
 *     <section id="limits" data-tabpanel>…</section>
 *   </timlee-tabs>
 *
 * With scripting off that is a perfectly ordinary table of contents: every
 * panel is visible and the links jump to them. With scripting on, the links
 * become `role="tab"`, the panels become `role="tabpanel"`, all but one panel
 * is hidden, and the roving tabindex / arrow-key behaviour from the WAI-ARIA
 * APG takes over.
 *
 * NON-OBVIOUS DECISIONS
 *  - The roles are applied by script, not by the template. Writing
 *    `role="tab"` into server HTML would announce a tab widget to a screen
 *    reader on a page where arrow keys do nothing, which is worse than
 *    announcing a list of links.
 *  - No shadow DOM: `aria-controls` and `aria-labelledby` run between the tab
 *    and its panel, and the panels are the page's actual content.
 *  - Activation follows the APG default for automatic tabs (selection follows
 *    focus); `manual` switches to explicit Enter/Space activation, which is
 *    what the APG recommends when a panel is expensive to render.
 *  - A hash in the URL selects the matching tab on load, so a deep link the
 *    server handed out works. Writing the hash back is opt-in (`sync-hash`),
 *    because a component that rewrites the page URL on every click fights
 *    with any router, and two tab sets on one page would fight each other.
 */

import { type Orientation, RovingTabindex } from './a11y';
import { define, ensureId, TimleeElement } from './base';

export class TimleeTabs extends TimleeElement {
  static observedAttributes = ['orientation'];

  #roving: RovingTabindex | null = null;
  #tabs: HTMLElement[] = [];

  get orientation(): Orientation {
    return this.getAttribute('orientation') === 'vertical' ? 'vertical' : 'horizontal';
  }

  get manual(): boolean {
    return this.hasAttribute('manual');
  }

  /** Write the selected panel's id back to the location hash. */
  get syncHash(): boolean {
    return this.hasAttribute('sync-hash');
  }

  get tablist(): HTMLElement | null {
    return this.querySelector<HTMLElement>('[data-tablist], [role="tablist"]');
  }

  get tabs(): HTMLElement[] {
    return this.#tabs;
  }

  get panels(): HTMLElement[] {
    return Array.from(this.querySelectorAll<HTMLElement>('[data-tabpanel], [role="tabpanel"]'));
  }

  /** The id of the selected panel. Settable. */
  get value(): string {
    return this.#selectedTab()?.getAttribute('aria-controls') ?? '';
  }
  set value(panelId: string) {
    const tab = this.#tabs.find((t) => t.getAttribute('aria-controls') === panelId);
    if (tab) this.select(tab, { focus: false });
  }

  protected override hydrate(): void {
    const tablist = this.tablist;
    if (!tablist) return;

    tablist.setAttribute('role', 'tablist');
    tablist.setAttribute('aria-orientation', this.orientation);

    this.#tabs = Array.from(
      tablist.querySelectorAll<HTMLElement>(':scope > a[href], :scope > button'),
    );
    if (!this.#tabs.length) return;

    for (const tab of this.#tabs) {
      const panel = this.#panelFor(tab);
      if (!panel) continue;
      tab.setAttribute('role', 'tab');
      ensureId(tab, 'timlee-tab');
      tab.setAttribute('aria-controls', ensureId(panel, 'timlee-tabpanel'));
      panel.setAttribute('role', 'tabpanel');
      panel.setAttribute('aria-labelledby', tab.id);
      // The panel is focusable so keyboard users can reach content that holds
      // no focusable element of its own, per the APG.
      if (!panel.hasAttribute('tabindex')) panel.tabIndex = 0;
    }

    this.#roving = new RovingTabindex({
      orientation: this.orientation,
      onActivate: (tab) => {
        if (!this.manual) this.select(tab, { focus: false });
      },
    });

    this.select(this.#initialTab(), { focus: false, silent: true });
  }

  protected override connected(signal: AbortSignal): void {
    const tablist = this.tablist;
    if (!tablist) return;
    tablist.addEventListener('keydown', this.#onKeydown, { signal });
    tablist.addEventListener('click', this.#onClick, { signal });
  }

  attributeChangedCallback(): void {
    if (!this.ready) return;
    this.tablist?.setAttribute('aria-orientation', this.orientation);
    if (this.#roving) this.#roving.orientation = this.orientation;
  }

  #onKeydown = (event: KeyboardEvent): void => {
    if (this.#roving?.handleKeydown(event)) return;
    if (event.key !== 'Enter' && event.key !== ' ') return;
    const tab = (event.target as Element | null)?.closest<HTMLElement>('[role="tab"]');
    if (!tab) return;
    event.preventDefault();
    this.select(tab, { focus: true });
  };

  #onClick = (event: MouseEvent): void => {
    const tab = (event.target as Element | null)?.closest<HTMLElement>('[role="tab"]');
    if (!tab || !this.#tabs.includes(tab)) return;
    // The href stays in the markup as the scriptless fallback, so the jump
    // has to be cancelled now that the panels are being switched instead.
    event.preventDefault();
    this.select(tab, { focus: true });
  };

  /** Select a tab, hide the other panels and tell the page about it. */
  select(tab: HTMLElement | null, options: { focus?: boolean; silent?: boolean } = {}): void {
    if (!tab || !this.#tabs.includes(tab)) return;

    for (const candidate of this.#tabs) {
      const selected = candidate === tab;
      candidate.setAttribute('aria-selected', String(selected));
      // aria-current was the scriptless marker; aria-selected supersedes it.
      candidate.removeAttribute('aria-current');
      const panel = this.#panelFor(candidate);
      if (panel) panel.hidden = !selected;
    }

    this.#roving?.setItems(this.#tabs, tab);
    if (options.focus) tab.focus();

    const panelId = tab.getAttribute('aria-controls') ?? '';
    if (!options.silent) {
      // replaceState, not pushState: a tab change is not a navigation, and
      // filling the back stack with them is a well-known annoyance.
      if (this.syncHash && panelId && location.hash !== `#${panelId}`) {
        history.replaceState(history.state, '', `#${panelId}`);
      }
      this.emit('timlee-tab-change', { tab, panelId });
    }
  }

  #selectedTab(): HTMLElement | null {
    return this.#tabs.find((t) => t.getAttribute('aria-selected') === 'true') ?? null;
  }

  #initialTab(): HTMLElement | null {
    const fromHash = location.hash.slice(1);
    return (
      (fromHash && this.#tabs.find((t) => this.#targetId(t) === fromHash)) ||
      this.#tabs.find((t) => t.getAttribute('aria-current') === 'page') ||
      this.#tabs.find((t) => t.getAttribute('aria-selected') === 'true') ||
      this.#tabs[0] ||
      null
    );
  }

  #targetId(tab: HTMLElement): string {
    const controls = tab.getAttribute('aria-controls');
    if (controls) return controls;
    const href = tab.getAttribute('href') ?? '';
    return href.startsWith('#') ? href.slice(1) : (tab.dataset.panel ?? '');
  }

  #panelFor(tab: HTMLElement): HTMLElement | null {
    const id = this.#targetId(tab);
    if (!id) return null;
    // Scoped to this element so nested tab sets cannot steal each other's
    // panels, with a document lookup for panels rendered outside the host.
    return this.querySelector<HTMLElement>(`#${CSS.escape(id)}`) ?? document.getElementById(id);
  }
}

define('timlee-tabs', TimleeTabs);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-tabs': TimleeTabs;
  }
}
