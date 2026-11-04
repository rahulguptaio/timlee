/**
 * <timlee-select> — a labelled native `<select>`.
 *
 * Deliberately *not* a custom listbox. A native select gets the platform's
 * own picker on touch, type-ahead, form submission, validation and screen
 * reader support for free, and none of that is worth re-implementing to gain
 * control over the option list's appearance. When a page genuinely needs rich
 * options, that is a different component (a combobox), not a skin on this one.
 *
 * See field.ts for the shared wiring and the no-shadow-DOM reasoning.
 */

import { define } from './base';
import { TimleeField } from './field';

export class TimleeSelect extends TimleeField {
  override get control(): HTMLSelectElement | null {
    return super.control as HTMLSelectElement | null;
  }

  /** The selected option's text, which is usually what callers actually want. */
  get selectedLabel(): string {
    const control = this.control;
    return control?.selectedOptions[0]?.textContent?.trim() ?? '';
  }
}

define('timlee-select', TimleeSelect);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-select': TimleeSelect;
  }
}
