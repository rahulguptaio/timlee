/**
 * <timlee-card> — a bordered surface with optional header and footer.
 *
 *   <timlee-card interactive>
 *     <header><h3><a href="/keys/42" data-card-link>Production key</a></h3></header>
 *     <p>Created 3 days ago.</p>
 *     <footer><timlee-badge tone="success">Active</timlee-badge></footer>
 *   </timlee-card>
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM and, apart from registration, no JavaScript. A card is
 *    layout and elevation; both are CSS, and a card that needs a script to
 *    appear is a card that flashes unstyled on a slow connection.
 *  - `interactive` makes the *whole* card clickable by stretching the marked
 *    link's `::after` over it, rather than putting a click handler on the
 *    card. The link stays the only focusable thing, keyboard activation is
 *    the link's own, middle-click and "copy link address" keep working, and
 *    text inside the card is still selectable.
 *  - Elevation in dark themes is a border plus `--ts-highlight`'s inset top
 *    line, never a larger shadow: against #0b1424 a drop shadow is invisible.
 *  - `<header>` and `<footer>` are stripped of their implicit roles. HTML only
 *    exempts them from being `banner`/`contentinfo` inside `article`, `aside`,
 *    `main`, `nav` or `section` — a custom element is none of those, so a page
 *    with three cards would otherwise expose three page banners.
 */

import { define, TimleeElement } from './base';

/** Sectioning elements whose presence demotes header/footer to generic. */
const SECTIONING = 'article, aside, main, nav, section, [role="article"], [role="region"]';

export class TimleeCard extends TimleeElement {
  protected override hydrate(): void {
    // Only when the card is not already inside sectioning content, and never
    // over a role the template set deliberately.
    if (this.closest(SECTIONING)) return;
    for (const node of this.querySelectorAll<HTMLElement>(':scope > header, :scope > footer')) {
      if (!node.hasAttribute('role')) node.setAttribute('role', 'none');
    }
  }
}

define('timlee-card', TimleeCard);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-card': TimleeCard;
  }
}
