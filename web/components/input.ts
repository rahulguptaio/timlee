/**
 * <timlee-input> — a labelled single-line text field.
 *
 * Everything that makes it work — label association, submission, constraint
 * validation, autofill — is the native `<input>` the server wrote. See
 * field.ts for the shared wiring and for why none of this uses shadow DOM.
 *
 * NON-OBVIOUS DECISION
 * The element exposes `value`, `error` and `checkValidity()` so calling code
 * never has to reach past it into the control, but it does not shadow the
 * control's own API — `field.control` is always the real element.
 */

import { define } from './base';
import { TimleeField } from './field';

export class TimleeInput extends TimleeField {
  override get control(): HTMLInputElement | null {
    return super.control as HTMLInputElement | null;
  }
}

define('timlee-input', TimleeInput);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-input': TimleeInput;
  }
}
