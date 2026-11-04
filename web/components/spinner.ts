/**
 * <timlee-spinner> and <timlee-progress> — indeterminate and determinate
 * progress.
 *
 *   <timlee-spinner label="Loading models"></timlee-spinner>
 *   <timlee-progress label="Uploading"><progress value="40" max="100"></progress></timlee-progress>
 *
 * NON-OBVIOUS DECISIONS
 *  - `<timlee-spinner>` draws itself entirely in CSS on the host, with no
 *    children at all. That is what makes a server-rendered spinner work: the
 *    Go template writes an empty element and it is already spinning before
 *    any script runs. It is also the one component where a shadow root would
 *    have cost nothing — there is no content, no form and no ARIA reference
 *    to preserve — and it still does not use one, because a shadow root could
 *    not be styled by the static stylesheet and would therefore show nothing
 *    until the bundle loaded. Server-rendered-first wins again.
 *  - `<timlee-progress>` wraps a native `<progress>` rather than drawing a
 *    bar, so the value is exposed to assistive technology by the platform and
 *    the element works with the value the server already knows.
 *  - Both respect `prefers-reduced-motion`: the spinner stops rotating and
 *    pulses instead, which still reads as "working" without the spin.
 *  - The spinner is `role="progressbar"` with an accessible name. A spinner
 *    with no name is announced as "progress bar" and nothing else.
 */

import { percent, t } from '../assets/ts/i18n';
import { define, ensureId, TimleeElement } from './base';

export class TimleeSpinner extends TimleeElement {
  static observedAttributes = ['label'];

  protected override hydrate(): void {
    this.setAttribute('role', 'progressbar');
    // Indeterminate: no aria-valuenow, which is exactly how a screen reader
    // is told "busy, duration unknown".
    this.#syncLabel();
  }

  attributeChangedCallback(): void {
    if (this.ready) this.#syncLabel();
  }

  #syncLabel(): void {
    if (this.hasAttribute('aria-labelledby')) return;
    this.setAttribute('aria-label', this.getAttribute('label') ?? t('components.spinner.loading'));
  }
}

export class TimleeProgress extends TimleeElement {
  get control(): HTMLProgressElement | null {
    return this.querySelector<HTMLProgressElement>(':scope > progress');
  }

  get value(): number | null {
    const control = this.control;
    if (!control) return null;
    // A <progress> with no value attribute is indeterminate, and its `value`
    // IDL attribute lies about that by reporting 0.
    return control.hasAttribute('value') ? control.value : null;
  }
  set value(next: number | null) {
    const control = this.control;
    if (!control) return;
    if (next === null) control.removeAttribute('value');
    else control.value = next;
    this.#syncText();
  }

  protected override hydrate(): void {
    const control = this.control;
    if (!control) return;

    const label = this.querySelector('label');
    if (label && !label.getAttribute('for')) {
      label.setAttribute('for', ensureId(control, 'timlee-progress'));
    } else if (!label && !control.hasAttribute('aria-label')) {
      control.setAttribute(
        'aria-label',
        this.getAttribute('label') ?? t('components.spinner.progress'),
      );
    }
    this.#syncText();
  }

  /** Percent text, when the author asked for it. Kept out of the live region. */
  #syncText(): void {
    if (!this.hasAttribute('show-value')) return;
    const control = this.control;
    if (!control) return;
    let text = this.querySelector<HTMLElement>('.timlee-progress__value');
    if (!text) {
      text = document.createElement('span');
      text.className = 'timlee-progress__label timlee-progress__value';
      this.append(text);
    }
    const value = this.value;
    text.textContent = value === null ? '' : percent(value / (control.max || 1));
  }
}

define('timlee-spinner', TimleeSpinner);
define('timlee-progress', TimleeProgress);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-spinner': TimleeSpinner;
    'timlee-progress': TimleeProgress;
  }
}
