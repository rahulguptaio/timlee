/**
 * <timlee-checkbox> and <timlee-radio> — styled native boxes.
 *
 *   <timlee-checkbox>
 *     <label>
 *       <input type="checkbox" name="tos" value="1">
 *       <span>I accept the terms</span>
 *     </label>
 *     <p data-hint>You can withdraw consent later.</p>
 *   </timlee-checkbox>
 *
 * WHAT IT IS FOR
 * The visual treatment, the hit area, and the indeterminate state — nothing
 * else. `accent-color` does the heavy lifting, so the control stays a real
 * checkbox: the platform's own focus ring, high-contrast-mode rendering,
 * forced-colors support and screen reader semantics all survive.
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM: form participation and `<label>` association, as in
 *    field.ts. A checkbox is the one control where people most often reach
 *    for `appearance: none` plus a div; that loses forced-colors rendering,
 *    so Timlee keeps the native box and only tunes size, colour and spacing.
 *  - `indeterminate` is an attribute here because it is IDL-only on the
 *    native input, so a Go template has no way to express it otherwise.
 *  - Radios are not grouped by this element. Grouping is `<fieldset>` with a
 *    `<legend>`, which is what assistive technology already announces; a
 *    custom `role="radiogroup"` wrapper would add nothing and could conflict.
 */

import { addToken, define, ensureId, TimleeElement } from './base';

/** Shared wiring: hint/error description and the native-only properties. */
abstract class TimleeChoice extends TimleeElement {
  static observedAttributes = ['indeterminate', 'invalid'];

  protected abstract get type(): 'checkbox' | 'radio';

  get control(): HTMLInputElement | null {
    return this.querySelector<HTMLInputElement>(`input[type="${this.type}"]`);
  }

  get checked(): boolean {
    return this.control?.checked ?? false;
  }
  set checked(value: boolean) {
    const control = this.control;
    if (control) control.checked = value;
  }

  get value(): string {
    return this.control?.value ?? '';
  }

  protected override hydrate(): void {
    const control = this.control;
    if (!control) return;

    const label = this.querySelector('label');
    // An implicit label (the input inside the <label>) already associates;
    // only an adjacent label needs the explicit `for`.
    if (label && !label.contains(control) && !label.getAttribute('for')) {
      label.setAttribute('for', ensureId(control, 'timlee-choice'));
    }

    for (const node of this.querySelectorAll<HTMLElement>(
      ':scope > [data-hint], :scope > [data-error]',
    )) {
      if (node.hasAttribute('data-error') && !node.textContent?.trim()) continue;
      addToken(control, 'aria-describedby', ensureId(node, 'timlee-choice-desc'));
    }

    this.#sync();
  }

  attributeChangedCallback(): void {
    if (this.ready) this.#sync();
  }

  #sync(): void {
    const control = this.control;
    if (!control) return;
    // `indeterminate` has no content attribute on the native input, so a
    // server-rendered page can only express it through this host attribute.
    control.indeterminate = this.hasAttribute('indeterminate');
    if (this.hasAttribute('invalid')) control.setAttribute('aria-invalid', 'true');
    else control.removeAttribute('aria-invalid');
  }
}

export class TimleeCheckbox extends TimleeChoice {
  protected get type(): 'checkbox' {
    return 'checkbox';
  }

  get indeterminate(): boolean {
    return this.hasAttribute('indeterminate');
  }
  set indeterminate(value: boolean) {
    this.toggleAttribute('indeterminate', value);
  }
}

export class TimleeRadio extends TimleeChoice {
  protected get type(): 'radio' {
    return 'radio';
  }
}

define('timlee-checkbox', TimleeCheckbox);
define('timlee-radio', TimleeRadio);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-checkbox': TimleeCheckbox;
    'timlee-radio': TimleeRadio;
  }
}
