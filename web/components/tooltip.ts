/**
 * <timlee-tooltip> — a short description attached to a control.
 *
 *   <timlee-tooltip>
 *     <timlee-button variant="ghost">
 *       <button type="button" aria-describedby="tt-rpm">Rate limit</button>
 *     </timlee-button>
 *     <span role="tooltip" id="tt-rpm">Requests per minute, per API key.</span>
 *   </timlee-tooltip>
 *
 * The trigger keeps whatever styling it already had: a bare `<button>` here
 * would render with the UA's own button chrome, which in a dark colour-scheme
 * does not reach 4.5:1 against inherited text. Wrap it, or use an element
 * that has no UA chrome at all.
 *
 * WHAT IT IS FOR
 * Supplementary text — never the only place a piece of information appears,
 * because tooltips are invisible to touch users and to anyone who does not
 * hover. Anything required to complete a task belongs in the page.
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM, and here it is not a preference: `aria-describedby` on
 *    the trigger must resolve to the tooltip element, and ID references do
 *    not cross a shadow boundary.
 *  - It works with no JavaScript at all. `:has()` on the host shows the
 *    tooltip on `:hover` and `:focus-within`, so the server's markup is a
 *    complete, usable tooltip before the bundle arrives.
 *  - JavaScript's only job is to move the tooltip into the top layer with the
 *    Popover API (Baseline 2024: Chrome 114, Safari 17, Firefox 125) so it
 *    escapes `overflow: hidden` and stacking contexts, and to add the
 *    Escape-to-dismiss the APG requires. Where `popover` is missing, the
 *    CSS-only behaviour simply stays in charge.
 *  - Positioning is measured on show rather than declared with CSS anchor
 *    positioning, which is still Chromium-only. The anchor API is the
 *    intended replacement once it is interoperable.
 */

import { define, ensureId, TimleeElement } from './base';

const SUPPORTS_POPOVER =
  typeof HTMLElement !== 'undefined' && typeof HTMLElement.prototype.togglePopover === 'function';

/** What counts as the focusable control inside a wrapper. */
const TRIGGER_SELECTOR = 'a[href], button, input, select, textarea, [tabindex]';

/**
 * APG: a tooltip appears after a short delay. It lingers rather than vanishing
 * on the spot, because WCAG 1.4.13 lets the pointer travel onto it to read or
 * magnify it, and a pointer leaving the control has to get there first.
 */
const OPEN_DELAY = 200;
const CLOSE_DELAY = 300;

export class TimleeTooltip extends TimleeElement {
  #openTimer = 0;
  #closeTimer = 0;
  #open = false;

  /**
   * The thing the tooltip describes.
   *
   * The first child is often a wrapper — `<timlee-button>` around a real
   * `<button>` — and `aria-describedby` has to land on the control the user
   * actually focuses, not on the box around it.
   */
  get trigger(): HTMLElement | null {
    const first = this.querySelector<HTMLElement>(':scope > :not([role="tooltip"])');
    if (!first) return null;
    if (first.matches(TRIGGER_SELECTOR)) return first;
    return first.querySelector<HTMLElement>(TRIGGER_SELECTOR) ?? first;
  }

  get tip(): HTMLElement | null {
    return this.querySelector<HTMLElement>(':scope > [role="tooltip"]');
  }

  get placement(): 'top' | 'bottom' {
    return this.getAttribute('placement') === 'bottom' ? 'bottom' : 'top';
  }

  protected override hydrate(): void {
    const trigger = this.trigger;
    const tip = this.tip;
    if (!trigger || !tip) return;

    const id = ensureId(tip, 'timlee-tooltip');
    if (!trigger.getAttribute('aria-describedby')?.includes(id)) {
      trigger.setAttribute('aria-describedby', id);
    }
    // A non-interactive trigger (an icon, a term) still has to be reachable.
    if (!trigger.matches(TRIGGER_SELECTOR)) {
      trigger.tabIndex = 0;
    }

    if (SUPPORTS_POPOVER) tip.setAttribute('popover', 'manual');
  }

  protected override connected(signal: AbortSignal): void {
    this.addEventListener('pointerenter', () => this.#scheduleOpen(), { signal });
    this.addEventListener('pointerleave', () => this.#scheduleClose(), { signal });
    this.addEventListener('focusin', () => this.#open || this.#show(), { signal });
    this.addEventListener('focusout', () => this.hide(), { signal });
    // APG: Escape dismisses a tooltip without moving focus off the control.
    this.addEventListener(
      'keydown',
      (event) => {
        if (event.key === 'Escape' && this.#open) {
          event.stopPropagation();
          this.hide();
        }
      },
      { signal },
    );
  }

  protected override disconnected(): void {
    clearTimeout(this.#openTimer);
    clearTimeout(this.#closeTimer);
  }

  #scheduleOpen(): void {
    clearTimeout(this.#closeTimer);
    if (this.#open) return;
    this.#openTimer = window.setTimeout(() => this.#show(), OPEN_DELAY);
  }

  #scheduleClose(): void {
    clearTimeout(this.#openTimer);
    this.#closeTimer = window.setTimeout(() => this.hide(), CLOSE_DELAY);
  }

  #show(): void {
    const tip = this.tip;
    if (!tip || this.#open) return;
    this.#open = true;
    if (SUPPORTS_POPOVER && tip.hasAttribute('popover')) {
      tip.showPopover();
      this.#position(tip);
    }
    this.setAttribute('open', '');
  }

  hide(): void {
    clearTimeout(this.#openTimer);
    clearTimeout(this.#closeTimer);
    const tip = this.tip;
    this.#open = false;
    this.removeAttribute('open');
    if (tip && SUPPORTS_POPOVER && tip.matches(':popover-open')) tip.hidePopover();
  }

  /** Measure and place. Clamped so a tooltip near an edge stays readable. */
  #position(tip: HTMLElement): void {
    const trigger = this.trigger ?? this;
    const anchor = trigger.getBoundingClientRect();
    const box = tip.getBoundingClientRect();
    const gap = 8;

    let top = this.placement === 'bottom' ? anchor.bottom + gap : anchor.top - box.height - gap;
    // Flip rather than overflow the viewport.
    if (top < 0) top = anchor.bottom + gap;
    else if (top + box.height > innerHeight) top = anchor.top - box.height - gap;

    const left = Math.min(
      Math.max(gap, anchor.left + anchor.width / 2 - box.width / 2),
      Math.max(gap, innerWidth - box.width - gap),
    );

    tip.style.top = `${Math.round(top)}px`;
    tip.style.left = `${Math.round(left)}px`;
    // Which side it ended up on, after any flip: the CSS hover bridge has to
    // span the gap on the side facing the control.
    tip.toggleAttribute('data-below', top > anchor.top);
  }
}

define('timlee-tooltip', TimleeTooltip);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-tooltip': TimleeTooltip;
  }
}
