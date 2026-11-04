/**
 * <timlee-button> — the chrome around a native button or link.
 *
 * WHAT IT IS FOR
 * The server writes a real `<button>` or `<a>`; this element supplies the
 * visual variant and the one behaviour HTML has no equivalent for — a loading
 * state that disables the control, marks it busy and shows a spinner without
 * the label reflowing.
 *
 *   <timlee-button variant="primary">
 *     <button type="submit">Save changes</button>
 *   </timlee-button>
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM. The control inside must submit its form and be reachable
 *    by `<label for>` / `aria-describedby` from elsewhere on the page. Both
 *    break across a shadow boundary, and neither is worth style encapsulation
 *    a tag-anchored selector already provides.
 *  - The element does not create the button. A `<timlee-button>` with no
 *    control inside is an authoring error, not a thing to paper over: if the
 *    element created its own, the page would have no button until the bundle
 *    loaded, which is the failure mode this whole library exists to avoid.
 *  - `loading` sets `disabled` on the control rather than `aria-disabled`.
 *    A busy submit button must actually stop submitting, and `aria-disabled`
 *    does not. Focus loss is accepted because the alternative — a second
 *    submit mid-flight — is worse.
 *  - Minimum target size is 2.25rem (36px) even at `size="sm"`, clearing
 *    WCAG 2.2 SC 2.5.8's 24×24 CSS-pixel floor with room to spare.
 */

import { define, reflectBool, TimleeElement } from './base';

export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'byoc' | 'link';
export type ButtonSize = 'sm' | 'md' | 'lg';

/** Shared by button and icon-button: the control the server wrote. */
const CONTROL_SELECTOR =
  'button, a[href], input[type="submit"], input[type="button"], input[type="reset"]';

export class TimleeButton extends TimleeElement {
  static observedAttributes = ['loading', 'disabled'];

  #spinner: HTMLSpanElement | null = null;

  /** The native control the server rendered. Null is an authoring error. */
  get control(): HTMLButtonElement | HTMLAnchorElement | HTMLInputElement | null {
    return this.querySelector<HTMLButtonElement>(`:scope > ${CONTROL_SELECTOR}`);
  }

  get loading(): boolean {
    return this.hasAttribute('loading');
  }
  set loading(value: boolean) {
    reflectBool(this, 'loading', value);
  }

  get disabled(): boolean {
    return this.hasAttribute('disabled');
  }
  set disabled(value: boolean) {
    reflectBool(this, 'disabled', value);
  }

  protected override hydrate(): void {
    const control = this.control;
    if (!control) return;

    // Wrap the server's label so `loading` can hide it without collapsing the
    // button's width. Done once; repeated hydration is guarded by the base.
    if (!control.querySelector(':scope > .timlee-button__label') && control.childNodes.length) {
      const label = document.createElement('span');
      label.className = 'timlee-button__label';
      label.append(...control.childNodes);
      control.append(label);
    }
    this.#sync();
  }

  attributeChangedCallback(): void {
    if (this.ready) this.#sync();
  }

  #sync(): void {
    const control = this.control;
    if (!control) return;

    const busy = this.loading;
    const off = busy || this.disabled;

    control.setAttribute('aria-busy', String(busy));

    if (control instanceof HTMLAnchorElement) {
      // A link has no `disabled`; `aria-disabled` plus removing it from the
      // tab order is the closest honest equivalent.
      control.setAttribute('aria-disabled', String(off));
      if (off) control.setAttribute('tabindex', '-1');
      else control.removeAttribute('tabindex');
    } else {
      control.toggleAttribute('disabled', off);
    }

    if (busy && !this.#spinner) {
      this.#spinner = document.createElement('span');
      this.#spinner.className = 'timlee-button__spinner';
      this.#spinner.setAttribute('aria-hidden', 'true');
      // Before the label, so the reading order is "spinner, Saving" and the
      // label keeps its place rather than jumping when the spinner appears.
      control.prepend(this.#spinner);
    } else if (!busy && this.#spinner) {
      this.#spinner.remove();
      this.#spinner = null;
    }
  }
}

export class TimleeIconButton extends TimleeElement {
  get control(): HTMLElement | null {
    return this.querySelector<HTMLElement>(`:scope > ${CONTROL_SELECTOR}`);
  }

  protected override hydrate(): void {
    const control = this.control;
    if (!control) return;

    // An icon-only control with no accessible name is unusable. Promote the
    // tooltip text when there is one; otherwise say so loudly in development.
    const named =
      control.hasAttribute('aria-label') ||
      control.hasAttribute('aria-labelledby') ||
      (control.textContent ?? '').trim().length > 0;

    if (!named) {
      const title = this.getAttribute('label') ?? control.getAttribute('title');
      if (title) control.setAttribute('aria-label', title);
      else if (__DEV__) {
        console.warn('[timlee-icon-button] control has no accessible name', this);
      }
    }
  }
}

define('timlee-button', TimleeButton);
define('timlee-icon-button', TimleeIconButton);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-button': TimleeButton;
    'timlee-icon-button': TimleeIconButton;
  }
}
