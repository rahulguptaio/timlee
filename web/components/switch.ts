/**
 * <timlee-switch> — an on/off control with two very different internals.
 *
 * PREFERRED FORM (works with JavaScript disabled):
 *   <timlee-switch>
 *     <label>
 *       <input type="checkbox" role="switch" name="streaming" checked>
 *       <span>Stream responses</span>
 *     </label>
 *   </timlee-switch>
 *
 * FALLBACK FORM (needs JavaScript, but participates in the form properly):
 *   <timlee-switch name="streaming" checked>Stream responses</timlee-switch>
 *
 * WHAT IT IS FOR
 * HTML has no switch element. `<input type="checkbox" role="switch">` is the
 * closest thing and is what Timlee's Go templates should emit, because it
 * submits, resets, validates and toggles with no script at all. The second
 * form exists for callers who only have a value, not a form field — and it is
 * where this library uses `ElementInternals`.
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM, even in the fallback form. `ElementInternals` and
 *    `static formAssociated = true` are what make an element form-associated;
 *    a shadow root is not required for either, and attaching one here would
 *    only sever the `<label>` association the first form depends on.
 *  - The fallback form sets real ARIA attributes (`role`, `aria-checked`)
 *    rather than `internals.role` / `internals.ariaChecked`. Internals-based
 *    semantics are invisible to `getAttribute`, to CSS and to the server,
 *    which makes them untestable and unstylable; the default-semantics win is
 *    not worth that here.
 *  - The native control uses `appearance: none` — unavoidable, since there is
 *    no native switch — so a `forced-colors` block restores a visible state
 *    distinction for Windows high-contrast users.
 */

import { define, reflectBool, TimleeElement } from './base';

export class TimleeSwitch extends TimleeElement {
  /** Makes the element eligible for form association. No shadow root needed. */
  static formAssociated = true;
  static observedAttributes = ['checked', 'disabled', 'value', 'name'];

  #internals: ElementInternals | null = null;
  #standalone = false;
  #track: HTMLSpanElement | null = null;
  /** The `checked` content attribute as the server wrote it: the reset value. */
  #defaultChecked = false;

  /** The native checkbox, when the server supplied one. */
  get control(): HTMLInputElement | null {
    return this.querySelector<HTMLInputElement>('input[type="checkbox"]');
  }

  /** True when there is no native control and this element *is* the field. */
  get standalone(): boolean {
    return this.#standalone;
  }

  get checked(): boolean {
    return this.#standalone ? this.hasAttribute('checked') : (this.control?.checked ?? false);
  }
  set checked(value: boolean) {
    if (this.#standalone) {
      reflectBool(this, 'checked', value);
      this.#syncStandalone();
    } else if (this.control) {
      this.control.checked = value;
    }
  }

  get disabled(): boolean {
    return this.#standalone ? this.hasAttribute('disabled') : (this.control?.disabled ?? false);
  }
  set disabled(value: boolean) {
    if (this.#standalone) reflectBool(this, 'disabled', value);
    else if (this.control) this.control.disabled = value;
  }

  get value(): string {
    return this.getAttribute('value') ?? this.control?.value ?? 'on';
  }

  get name(): string {
    return this.getAttribute('name') ?? this.control?.name ?? '';
  }

  get form(): HTMLFormElement | null {
    return this.#internals?.form ?? this.control?.form ?? null;
  }

  protected override hydrate(): void {
    this.#standalone = this.control === null;
    this.#defaultChecked = this.hasAttribute('checked');
    if (!this.#standalone) return;

    // No native control: become one. `attachInternals` is only called in this
    // branch so the common, scriptless path stays a plain checkbox.
    this.#internals = this.attachInternals();
    this.setAttribute('data-timlee-standalone', '');
    this.setAttribute('role', 'switch');
    if (!this.hasAttribute('tabindex')) this.tabIndex = 0;

    this.#track = document.createElement('span');
    this.#track.className = 'timlee-switch__track';
    this.#track.setAttribute('aria-hidden', 'true');
    this.prepend(this.#track);

    this.#syncStandalone();
  }

  protected override connected(signal: AbortSignal): void {
    if (this.#standalone) {
      this.addEventListener('click', this.#onClick, { signal });
      this.addEventListener('keydown', this.#onKeydown, { signal });
      return;
    }
    // Native mode: the input already does everything; only re-broadcast the
    // change so both forms expose the same event to callers.
    this.control?.addEventListener(
      'change',
      () => this.emit('timlee-change', { checked: this.checked }),
      { signal },
    );
  }

  attributeChangedCallback(): void {
    if (this.ready && this.#standalone) this.#syncStandalone();
  }

  #onClick = (event: MouseEvent): void => {
    if (this.disabled) return;
    // A click on a nested link or button is that control's, not a toggle.
    if ((event.target as Element)?.closest('a[href], button')) return;
    this.toggle();
  };

  #onKeydown = (event: KeyboardEvent): void => {
    // APG: Space toggles. Enter is accepted too because a switch rendered as
    // a row in a settings list is routinely activated with it.
    if (event.key !== ' ' && event.key !== 'Enter') return;
    if (this.disabled) return;
    event.preventDefault();
    this.toggle();
  };

  /** Flip the switch as a user would, firing `change` and `timlee-change`. */
  toggle(): void {
    if (this.disabled) return;
    this.checked = !this.checked;
    this.dispatchEvent(new Event('change', { bubbles: true }));
    this.emit('timlee-change', { checked: this.checked });
  }

  #syncStandalone(): void {
    this.setAttribute('aria-checked', String(this.checked));
    this.setAttribute('aria-disabled', String(this.disabled));
    this.tabIndex = this.disabled ? -1 : 0;
    // An unchecked checkbox submits nothing; mirror that exactly.
    this.#internals?.setFormValue(this.checked ? this.value : null);
  }

  /* --- Form-associated lifecycle ----------------------------------------- */

  formResetCallback(): void {
    // Native checkboxes reset to their content attribute; this mirrors that,
    // using the value the server wrote rather than the current one.
    this.checked = this.#defaultChecked;
  }

  /** Back/forward navigation and session restore hand the old state back. */
  formStateRestoreCallback(state: string | null): void {
    this.checked = state === this.value;
  }

  formDisabledCallback(disabled: boolean): void {
    if (this.#standalone) reflectBool(this, 'disabled', disabled);
  }
}

define('timlee-switch', TimleeSwitch);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-switch': TimleeSwitch;
  }
}
