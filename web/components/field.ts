/**
 * field.ts — the shared behaviour of <timlee-input>, <timlee-textarea> and
 * <timlee-select>.
 *
 * WHAT IT IS FOR
 * A labelled form control with a hint and an error message, wired together
 * correctly. The server writes all four parts as ordinary HTML:
 *
 *   <timlee-input>
 *     <label for="email">Work email</label>
 *     <input id="email" name="email" type="email" required>
 *     <p data-hint>We only use this for billing.</p>
 *     <p data-error>Enter a valid address.</p>   <!-- omitted when valid -->
 *   </timlee-input>
 *
 * The control submits, validates and autofills with no JavaScript at all. The
 * element's job is the wiring HTML cannot express declaratively:
 * `aria-describedby` pointing at both the hint and the error, `aria-invalid`
 * derived from the error's presence, and an id for the label to target when
 * the template did not supply one.
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM, and this is the clearest case in the library. A shadow
 *    root would cut `<label for>` and `aria-describedby` — both of which only
 *    resolve within one tree — and would take the control out of its form.
 *    Re-creating submission, validation, reset and autofill through
 *    `ElementInternals` to gain style scoping is a bad trade.
 *  - `aria-describedby` is *appended to*, never replaced. A page that already
 *    points a field at a shared "required field" note keeps that reference.
 *  - The error element is observed, not read once: Go re-renders the field
 *    on a failed POST, and client validation writes into the same node.
 */

import { addToken, ensureId, reflectBool, removeToken, TimleeElement } from './base';

export type FieldControl = HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;

const CONTROL_SELECTOR = 'input:not([type="checkbox"]):not([type="radio"]), textarea, select';

/**
 * Base class for the three wrapper fields. Subclasses add their own extras
 * (autosize, counters) but all of the ARIA wiring lives here.
 */
export abstract class TimleeField extends TimleeElement {
  static observedAttributes = ['invalid'];

  #observer: MutationObserver | null = null;
  /** True when the current `[invalid]` was derived from the error text. */
  #derived = false;

  get control(): FieldControl | null {
    return this.querySelector<FieldControl>(`:scope > ${CONTROL_SELECTOR}`);
  }

  get label(): HTMLLabelElement | null {
    return this.querySelector<HTMLLabelElement>(':scope > label');
  }

  get hintElement(): HTMLElement | null {
    return this.querySelector<HTMLElement>(':scope > [data-hint]');
  }

  get errorElement(): HTMLElement | null {
    return this.querySelector<HTMLElement>(':scope > [data-error]');
  }

  /** The current error text, or the empty string. Settable. */
  get error(): string {
    return (this.errorElement?.textContent ?? '').trim();
  }
  set error(message: string) {
    let node = this.errorElement;
    if (!node) {
      node = document.createElement('p');
      node.setAttribute('data-error', '');
      this.append(node);
      this.#wire();
    }
    node.textContent = message;
    this.syncValidity();
  }

  get invalid(): boolean {
    return this.hasAttribute('invalid');
  }
  set invalid(value: boolean) {
    reflectBool(this, 'invalid', value);
  }

  get value(): string {
    return this.control?.value ?? '';
  }
  set value(next: string) {
    const control = this.control;
    if (control) control.value = next;
  }

  get name(): string {
    return this.control?.name ?? '';
  }

  /** Native constraint validation, forwarded so callers need not reach in. */
  checkValidity(): boolean {
    return this.control?.checkValidity() ?? true;
  }

  reportValidity(): boolean {
    return this.control?.reportValidity() ?? true;
  }

  protected override hydrate(): void {
    this.#wire();
    this.syncValidity();
    this.enhance();
  }

  protected override connected(signal: AbortSignal): void {
    // Go re-renders the error paragraph on a failed POST and client code
    // writes into it too, so its text is watched rather than read once.
    this.#observer?.disconnect();
    this.#observer = new MutationObserver(() => {
      this.#wire();
      this.syncValidity();
    });
    this.#observer.observe(this, { childList: true, characterData: true, subtree: true });
    signal.addEventListener('abort', () => this.#observer?.disconnect(), { once: true });
  }

  attributeChangedCallback(): void {
    if (this.ready) this.syncValidity();
  }

  /** Subclass hook for behaviour beyond the shared wiring. */
  protected enhance(): void {}

  #wire(): void {
    const control = this.control;
    if (!control) return;

    const id = ensureId(control, 'timlee-field');
    const label = this.label;
    if (label && !label.getAttribute('for')) label.setAttribute('for', id);

    for (const node of this.querySelectorAll<HTMLElement>(
      ':scope > [data-hint], :scope > [data-error]',
    )) {
      const describedBy = ensureId(
        node,
        node.hasAttribute('data-error') ? 'timlee-error' : 'timlee-hint',
      );
      if (node.hasAttribute('data-error') && !node.textContent?.trim()) {
        removeToken(control, 'aria-describedby', describedBy);
      } else {
        addToken(control, 'aria-describedby', describedBy);
      }
    }
  }

  /**
   * Keep `[invalid]` on the host and `aria-invalid` on the control in step.
   *
   * The attribute has two possible authors: the error text, and the page. A
   * flag records which one set it, so clearing the error clears the state it
   * created without also discarding an `invalid` the page set deliberately.
   */
  protected syncValidity(): void {
    const control = this.control;
    if (!control) return;

    const hasError = this.error.length > 0;
    const setByPage = this.hasAttribute('invalid') && !this.#derived;
    const invalid = hasError || setByPage;

    this.#derived = hasError;
    reflectBool(this, 'invalid', invalid);
    if (invalid) control.setAttribute('aria-invalid', 'true');
    else control.removeAttribute('aria-invalid');
  }
}
