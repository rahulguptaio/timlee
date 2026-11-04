/**
 * <timlee-badge> — a status pill, optionally removable (a "chip").
 *
 *   <timlee-badge tone="success" dot>Active</timlee-badge>
 *   <timlee-badge tone="neutral" removable>gpt-4o-mini</timlee-badge>
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM: a badge is almost always inside a sentence, a table cell
 *    or a card header, and it is frequently the target of an
 *    `aria-describedby` from the row it annotates.
 *  - Tone is never the only signal. `dot` adds a shape, and the label itself
 *    always carries the meaning in words, so the component cannot fail
 *    WCAG 1.4.1 (use of colour) by construction.
 *  - The remove control is injected rather than written by the server. It is
 *    useless without JavaScript — there is nothing for it to do — so shipping
 *    it in the HTML would be a dead control on a scriptless page.
 */

import { t } from '../assets/ts/i18n';
import { define, TimleeElement } from './base';

export class TimleeBadge extends TimleeElement {
  #remove: HTMLButtonElement | null = null;

  protected override hydrate(): void {
    if (!this.hasAttribute('removable')) return;
    if (this.querySelector('.timlee-badge__remove')) return;

    const label = (
      this.getAttribute('remove-label') ??
      t('components.badge.remove', { label: this.textContent?.trim() ?? '' })
    ).trim();
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'timlee-badge__remove';
    button.setAttribute('aria-label', label);
    button.textContent = '\u00d7';
    this.append(button);
    this.#remove = button;
  }

  protected override connected(signal: AbortSignal): void {
    this.#remove?.addEventListener(
      'click',
      () => {
        // Cancelable: a list that owns its own state can preventDefault and
        // remove the badge itself after a round trip to the server.
        if (
          this.emit('timlee-remove', {
            value: this.getAttribute('value') ?? this.textContent?.trim(),
          })
        ) {
          this.remove();
        }
      },
      { signal },
    );
  }
}

define('timlee-badge', TimleeBadge);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-badge': TimleeBadge;
  }
}
