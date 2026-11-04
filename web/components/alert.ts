/**
 * <timlee-alert> — an inline banner: a validation summary, a quota warning,
 * a maintenance notice.
 *
 *   <timlee-alert tone="warning" dismissible>
 *     <strong>Approaching your quota.</strong>
 *     <p>You have used 92% of this month's tokens.</p>
 *   </timlee-alert>
 *
 * An alert is the page talking about the page. A toast is the page talking
 * about something that just happened; if the message should survive a
 * reload, it is an alert.
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM: alerts routinely contain links and are often referenced
 *    by `aria-describedby` from the form they belong to.
 *  - The role depends on when the alert arrived. One rendered by Go into the
 *    initial HTML gets no live-region role at all — a live region only
 *    announces *changes*, and claiming `role="alert"` on first paint either
 *    does nothing or interrupts the page being read. One inserted later gets
 *    `role="alert"` (danger) or `role="status"`.
 *  - The icon is a CSS shape keyed to the tone and never the only signal;
 *    the text always says what happened.
 */

import { t } from '../assets/ts/i18n';
import { define, TimleeElement } from './base';

export type AlertTone = 'neutral' | 'info' | 'success' | 'warning' | 'danger';

/** True while registration upgrades the alerts already in the document. */
let upgradingExisting = false;

export class TimleeAlert extends TimleeElement {
  #dismiss: HTMLButtonElement | null = null;

  get tone(): AlertTone {
    return (this.getAttribute('tone') as AlertTone) ?? 'neutral';
  }

  protected override hydrate(): void {
    // Was this in the initial HTML, or did script just insert it? Only the
    // second case is a change worth announcing. An alert that was already in
    // the document when this module was registered is the first case, even
    // when the module itself loads after the page has finished loading.
    const arrivedLate = !upgradingExisting && document.readyState === 'complete';
    if (!this.hasAttribute('role') && arrivedLate) {
      this.setAttribute('role', this.tone === 'danger' ? 'alert' : 'status');
    }

    if (this.hasAttribute('dismissible') && !this.querySelector('.timlee-alert__dismiss')) {
      const button = document.createElement('button');
      button.type = 'button';
      button.className = 'timlee-alert__dismiss';
      button.setAttribute(
        'aria-label',
        this.getAttribute('dismiss-label') ?? t('components.alert.dismiss'),
      );
      button.textContent = '\u00d7';
      this.append(button);
      this.#dismiss = button;
    }
  }

  protected override connected(signal: AbortSignal): void {
    this.#dismiss?.addEventListener(
      'click',
      () => {
        if (this.emit('timlee-dismiss')) this.remove();
      },
      { signal },
    );
  }
}

upgradingExisting = true;
define('timlee-alert', TimleeAlert);
upgradingExisting = false;

declare global {
  interface HTMLElementTagNameMap {
    'timlee-alert': TimleeAlert;
  }
}
