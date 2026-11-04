/**
 * <timlee-toaster> / <timlee-toast> — transient messages, including the
 * server's flash messages.
 *
 *   <timlee-toaster position="bottom-end">
 *     <timlee-toast tone="success">API key created.</timlee-toast>
 *   </timlee-toaster>
 *
 * WHAT IT IS FOR
 * A Go handler that just processed a POST wants to say "saved". That is a
 * flash message, and it belongs in the HTML of the next response — which is
 * why `<timlee-toast>` is a styled element that renders from markup, not a
 * JavaScript-only queue. `TimleeToaster.show()` exists for client-side events
 * on top of that, not instead of it.
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM. The message often contains a link ("View key"), and the
 *    toast's text must be readable by assistive technology in the same tree.
 *  - A toast is not a live region of its own. A `role="status"` inserted
 *    with its text already inside is announced unreliably (VoiceOver above
 *    all), so a toast that arrives after the page has loaded is spoken
 *    through the page's one polite announcer (a11y.ts), which exists before
 *    anything is said into it. A `danger` toast gets `role="alert"` instead:
 *    an inserted alert is announced everywhere, and an error that waits for
 *    the user to finish typing is an error they will miss. A flash message
 *    in the initial HTML is read with the page, as an alert would be.
 *  - The toaster is a manual popover while it holds toasts, so it shows
 *    above menus and drawers in the top layer rather than under them.
 *    `inline` keeps it in the page instead, for a panel or a story.
 *  - While a modal dialog is open the page behind it is inert. A toast that
 *    arrives then waits for the dialog to close, or to leave the page, before
 *    it shows, speaks or starts its countdown, so it is never raised where
 *    nobody can reach it.
 *  - Auto-dismiss pauses on hover and on focus-within, and is skipped
 *    entirely for `role="alert"` toasts and when the user prefers reduced
 *    motion — WCAG 2.2.1 requires content not to disappear out from under
 *    someone who is still reading or interacting with it.
 *  - The dismiss button is injected, not server-rendered: with no JavaScript
 *    it would do nothing.
 */

import { t } from '../assets/ts/i18n';
import { announce } from './announce';
import { define, prefersReducedMotion, TimleeElement } from './base';

export type ToastTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info';

const DEFAULT_DURATION = 6000;

const SUPPORTS_POPOVER = typeof HTMLElement !== 'undefined' && 'popover' in HTMLElement.prototype;

/** True while registration upgrades the toasts already in the document. */
let upgradingExisting = false;

/**
 * Run `task` once no modal dialog is open: until then the rest of the page
 * is inert, and whatever a toast says or shows there reaches nobody.
 *
 * A modal removed from the page while open (a page script swapping content,
 * dialog and all) never fires `close`, so its removal lets the task go too.
 */
function whenNoModal(task: () => void): void {
  const modal = document.querySelector('dialog:modal');
  if (!modal) {
    task();
    return;
  }
  const observer = new MutationObserver(() => {
    if (!modal.isConnected) release();
  });
  const release = () => {
    observer.disconnect();
    modal.removeEventListener('close', release);
    whenNoModal(task);
  };
  modal.addEventListener('close', release, { once: true });
  observer.observe(document, { childList: true, subtree: true });
}

export class TimleeToast extends TimleeElement {
  #timer = 0;
  #remaining = 0;
  #startedAt = 0;

  get tone(): ToastTone {
    return (this.getAttribute('tone') as ToastTone) ?? 'neutral';
  }

  /** Milliseconds before auto-dismiss; 0 or `danger` means "stay". */
  get duration(): number {
    const raw = Number(this.getAttribute('duration'));
    if (Number.isFinite(raw) && raw >= 0) return raw;
    return this.tone === 'danger' ? 0 : DEFAULT_DURATION;
  }

  protected override hydrate(): void {
    // Only news is announced: a toast in the HTML the page arrived with is
    // read with the page.
    const arrivedLate = !upgradingExisting && document.readyState !== 'loading';
    const message = (this.textContent ?? '').replace(/\s+/g, ' ').trim();

    if (!this.hasAttribute('no-dismiss') && !this.querySelector('.timlee-toast__dismiss')) {
      const dismiss = document.createElement('button');
      dismiss.type = 'button';
      dismiss.className = 'timlee-toast__dismiss';
      dismiss.setAttribute(
        'aria-label',
        this.getAttribute('dismiss-label') ?? t('components.toast.dismiss'),
      );
      dismiss.textContent = '\u00d7';
      this.append(dismiss);
    }

    whenNoModal(() => {
      if (!this.isConnected) return;
      if (this.hasAttribute('role')) {
        // The page chose how this one is announced.
      } else if (this.tone === 'danger') {
        this.setAttribute('role', 'alert');
      } else if (arrivedLate) {
        announce(message);
      }
      this.#start();
    });
  }

  protected override connected(signal: AbortSignal): void {
    this.querySelector('.timlee-toast__dismiss')?.addEventListener(
      'click',
      () => this.dismiss('user'),
      { signal },
    );
    // WCAG 2.2.1: reading or interacting must extend the time available.
    this.addEventListener('pointerenter', () => this.#pause(), { signal });
    this.addEventListener('pointerleave', () => this.#resume(), { signal });
    this.addEventListener('focusin', () => this.#pause(), { signal });
    this.addEventListener('focusout', () => this.#resume(), { signal });
  }

  protected override disconnected(): void {
    clearTimeout(this.#timer);
  }

  #start(): void {
    const duration = this.duration;
    // An assertive toast is an error message; it does not evaporate.
    if (!duration || this.getAttribute('role') === 'alert' || prefersReducedMotion()) return;
    this.#remaining = duration;
    this.#resume();
  }

  #pause(): void {
    if (!this.#timer) return;
    clearTimeout(this.#timer);
    this.#timer = 0;
    this.#remaining -= Date.now() - this.#startedAt;
  }

  #resume(): void {
    if (this.#timer || this.#remaining <= 0) return;
    this.#startedAt = Date.now();
    this.#timer = window.setTimeout(() => this.dismiss('timeout'), this.#remaining);
  }

  /** Remove the toast. Cancelable via the `timlee-toast-dismiss` event. */
  dismiss(reason: 'user' | 'timeout' | 'api' = 'api'): void {
    clearTimeout(this.#timer);
    this.#timer = 0;
    if (this.emit('timlee-toast-dismiss', { reason })) this.remove();
  }
}

export interface ToastOptions {
  tone?: ToastTone;
  duration?: number;
  dismissible?: boolean;
}

export class TimleeToaster extends TimleeElement {
  protected override hydrate(): void {
    // A labelled region is how a keyboard user finds the notifications again
    // after they have been announced.
    if (!this.hasAttribute('role')) this.setAttribute('role', 'region');
    if (!this.hasAttribute('aria-label')) {
      this.setAttribute('aria-label', t('components.toast.notifications'));
    }
    if (SUPPORTS_POPOVER && !this.hasAttribute('inline')) this.popover = 'manual';
  }

  protected override connected(signal: AbortSignal): void {
    if (!this.hasAttribute('popover')) return;
    // However toasts come and go — show(), a dismissal, markup swapped in by
    // a page script — the toaster follows them in and out of the top layer.
    const observer = new MutationObserver(() => this.#raise());
    observer.observe(this, { childList: true });
    signal.addEventListener('abort', () => observer.disconnect(), { once: true });
    this.#raise();
  }

  #raise(): void {
    if (!this.querySelector(':scope > timlee-toast')) {
      if (this.matches(':popover-open')) this.hidePopover();
      return;
    }
    // Raised over an open modal, it would sit above a dialog while being
    // part of the inert page behind it.
    whenNoModal(() => {
      if (
        this.isConnected &&
        !this.matches(':popover-open') &&
        this.querySelector(':scope > timlee-toast')
      ) {
        this.showPopover();
      }
    });
  }

  /** Append a toast. Returns it so callers can dismiss it early. */
  show(message: string | Node, options: ToastOptions = {}): TimleeToast {
    const toast = document.createElement('timlee-toast');
    if (options.tone) toast.setAttribute('tone', options.tone);
    if (options.duration !== undefined) toast.setAttribute('duration', String(options.duration));
    if (options.dismissible === false) toast.setAttribute('no-dismiss', '');
    toast.append(message);
    this.append(toast);
    return toast;
  }

  /** The page's toaster, created on demand for client-only notifications. */
  static current(): TimleeToaster {
    const existing = document.querySelector('timlee-toaster');
    if (existing) return existing;
    const toaster = document.createElement('timlee-toaster');
    document.body.append(toaster);
    return toaster;
  }
}

upgradingExisting = true;
define('timlee-toast', TimleeToast);
upgradingExisting = false;
define('timlee-toaster', TimleeToaster);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-toast': TimleeToast;
    'timlee-toaster': TimleeToaster;
  }
}
