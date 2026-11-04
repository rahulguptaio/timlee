/**
 * a11y.ts — the accessibility behaviours more than one component needs.
 *
 * Nothing here renders. These are the WAI-ARIA APG keyboard patterns and the
 * naming and motion helpers, kept in one place so `tabs`, `code-block` and
 * `table` cannot drift from each other. Announcements live in announce.ts.
 */

import { prefersReducedMotion } from './base';

export type Orientation = 'horizontal' | 'vertical';

/**
 * Roving tabindex, per the APG: exactly one item in the set is in the tab
 * order, arrow keys move between them, Home/End jump to the ends.
 *
 * Used by tabs. Deliberately *not* used by anything built out of links or
 * buttons that should each be tabbable (pagination, breadcrumb) — a roving
 * tabindex on a list of ordinary links makes them harder to reach, not easier.
 */
export class RovingTabindex {
  #items: HTMLElement[] = [];
  #orientation: Orientation;
  #onActivate: (item: HTMLElement, viaKeyboard: boolean) => void;
  /** Wrap at the ends, which APG specifies as optional but expects for tabs. */
  #wrap: boolean;

  constructor(options: {
    orientation?: Orientation;
    wrap?: boolean;
    onActivate: (item: HTMLElement, viaKeyboard: boolean) => void;
  }) {
    this.#orientation = options.orientation ?? 'horizontal';
    this.#wrap = options.wrap ?? true;
    this.#onActivate = options.onActivate;
  }

  set orientation(value: Orientation) {
    this.#orientation = value;
  }

  get items(): HTMLElement[] {
    return this.#items;
  }

  /** Replace the managed set and make `selected` the single tabbable item. */
  setItems(items: HTMLElement[], selected: HTMLElement | null): void {
    this.#items = items;
    this.focusTarget(selected ?? items[0] ?? null);
  }

  /** Make exactly one item tabbable without moving focus. */
  focusTarget(target: HTMLElement | null): void {
    for (const item of this.#items) {
      item.tabIndex = item === target && !isDisabled(item) ? 0 : -1;
    }
  }

  /** Returns true when the key was consumed. */
  handleKeydown(event: KeyboardEvent): boolean {
    const current = this.#items.indexOf(event.target as HTMLElement);
    if (current === -1) return false;

    const rtl =
      this.#orientation === 'horizontal' &&
      getComputedStyle(this.#items[current] as HTMLElement).direction === 'rtl';
    const next = this.#orientation === 'vertical' ? 'ArrowDown' : rtl ? 'ArrowLeft' : 'ArrowRight';
    const prev = this.#orientation === 'vertical' ? 'ArrowUp' : rtl ? 'ArrowRight' : 'ArrowLeft';

    let index: number | null = null;
    switch (event.key) {
      case next:
        index = this.#step(current, 1);
        break;
      case prev:
        index = this.#step(current, -1);
        break;
      case 'Home':
        index = this.#step(-1, 1);
        break;
      case 'End':
        index = this.#step(this.#items.length, -1);
        break;
      default:
        return false;
    }

    if (index === null) return false;
    const item = this.#items[index];
    if (!item) return false;
    event.preventDefault();
    this.focusTarget(item);
    item.focus();
    this.#onActivate(item, true);
    return true;
  }

  #step(from: number, delta: number): number | null {
    const count = this.#items.length;
    if (count === 0) return null;
    let index = from;
    for (let moved = 0; moved < count; moved++) {
      index += delta;
      if (index < 0 || index >= count) {
        if (!this.#wrap) return null;
        index = (index + count) % count;
      }
      const candidate = this.#items[index];
      if (candidate && !isDisabled(candidate)) return index;
    }
    return null;
  }
}

export function isDisabled(el: HTMLElement): boolean {
  return el.hasAttribute('disabled') || el.getAttribute('aria-disabled') === 'true';
}

/**
 * `el`'s name, numbered when another element matching `selector` would have
 * the same one, e.g. "json example 2".
 *
 * Landmark lists identify a region by its name alone, so two scrolling code
 * samples both called "json example, scrollable" cannot be told apart (axe
 * landmark-unique). Peers are numbered in document order from their markup,
 * whether or not they scroll, so the numbers do not depend on viewport width.
 */
export function distinctName(
  el: Element,
  selector: string,
  nameOf: (peer: Element) => string,
): string {
  const name = nameOf(el);
  const same = Array.from(document.querySelectorAll(selector)).filter(
    (peer) => nameOf(peer) === name,
  );
  return same.length > 1 ? `${name} ${same.indexOf(el) + 1}` : name;
}

/**
 * Run `offer` if, once the first layouts have had their chance, `el` is still
 * unmeasured because a `content-visibility: auto` ancestor is skipping it.
 *
 * Off-screen skipped content is never laid out, so no ResizeObserver reports
 * its size until the reader nears it, yet the keyboard and assistive
 * technology can reach it already, and an audit that measures it forces the
 * layout it would have had. A scroll region there has to be offered before
 * it can be measured; the observer settles it once the content renders.
 */
export function whenSkipped(
  el: Element,
  measured: () => boolean,
  offer: () => void,
  signal: AbortSignal,
): void {
  // Two frames: by then content near the viewport has been laid out and
  // observed, so only what the browser is really skipping is left.
  requestAnimationFrame(() =>
    requestAnimationFrame(() => {
      if (signal.aborted || measured() || typeof el.checkVisibility !== 'function') return;
      if (el.checkVisibility() && !el.checkVisibility({ contentVisibilityAuto: true })) offer();
    }),
  );
}

/* ------------------------------------------------------------ transition -- */

/**
 * Resolve after the element's CSS transitions/animations settle, or
 * immediately when the user prefers reduced motion.
 *
 * `timeout` is a backstop: `transitionend` never fires for a property that did
 * not actually change, and an element that is `display:none` by the time the
 * frame runs transitions nothing at all.
 */
export function afterMotion(el: Element, timeout = 400): Promise<void> {
  if (prefersReducedMotion()) return Promise.resolve();
  return new Promise((resolve) => {
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      el.removeEventListener('transitionend', finish);
      el.removeEventListener('animationend', finish);
      resolve();
    };
    el.addEventListener('transitionend', finish);
    el.addEventListener('animationend', finish);
    setTimeout(finish, timeout);
  });
}
