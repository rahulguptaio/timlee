/**
 * <timlee-textarea> — a labelled multi-line field, optionally self-sizing,
 * optionally with a character counter.
 *
 *   <timlee-textarea autosize counter>
 *     <label for="notes">Notes</label>
 *     <textarea id="notes" name="notes" maxlength="280" rows="3"></textarea>
 *   </timlee-textarea>
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM, for the reasons in field.ts.
 *  - `autosize` is a JavaScript fallback for `field-sizing: content`, which is
 *    Chrome/Edge 123+ only. The CSS wins where it exists; the script is
 *    harmless there because it only ever *raises* the height, and
 *    `field-sizing` already made the element tall enough.
 *  - The counter is `aria-live="polite"` but only announces near the limit.
 *    A screen reader reading a number after every keystroke is noise; the
 *    useful moment is "20 characters left", not "137".
 */

import { define } from './base';
import { TimleeField } from './field';

/** Announce only once the user is within this many characters of the limit. */
const ANNOUNCE_WITHIN = 20;

export class TimleeTextarea extends TimleeField {
  override get control(): HTMLTextAreaElement | null {
    return super.control as HTMLTextAreaElement | null;
  }

  #counter: HTMLElement | null = null;

  get autosize(): boolean {
    return this.hasAttribute('autosize');
  }

  protected override enhance(): void {
    const control = this.control;
    if (!control) return;

    if (this.hasAttribute('counter') && control.maxLength > 0) {
      this.#counter = document.createElement('span');
      this.#counter.className = 'timlee-field__counter';
      this.#counter.setAttribute('aria-live', 'polite');
      this.append(this.#counter);
    }

    this.#sync();
    if (this.autosize) this.#resize();
  }

  protected override connected(signal: AbortSignal): void {
    super.connected(signal);
    this.control?.addEventListener('input', () => this.#sync(), { signal });
  }

  #sync(): void {
    if (this.autosize) this.#resize();
    this.#updateCounter();
  }

  #updateCounter(): void {
    const control = this.control;
    if (!this.#counter || !control) return;
    const used = control.value.length;
    const max = control.maxLength;
    const left = max - used;
    this.#counter.textContent = `${used} / ${max}`;
    this.#counter.toggleAttribute('data-over', left < 0);
    // Silence the live region until the limit is actually in sight.
    this.#counter.setAttribute('aria-live', left <= ANNOUNCE_WITHIN ? 'polite' : 'off');
  }

  #resize(): void {
    const control = this.control;
    if (!control) return;
    control.style.height = 'auto';
    control.style.height = `${control.scrollHeight}px`;
  }
}

define('timlee-textarea', TimleeTextarea);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-textarea': TimleeTextarea;
  }
}
