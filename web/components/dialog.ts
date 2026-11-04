/**
 * <timlee-dialog> — the chrome and the open/close plumbing around a native
 * `<dialog>`.
 *
 *   <timlee-dialog id="revoke">
 *     <dialog aria-labelledby="revoke-title">
 *       <form method="dialog">
 *         <h2 id="revoke-title">Revoke this key?</h2>
 *         <p>Requests using it will start failing within 30 seconds.</p>
 *         <menu>
 *           <timlee-button><button value="cancel" autofocus>Cancel</button></timlee-button>
 *           <timlee-button variant="danger"><button value="revoke">Revoke</button></timlee-button>
 *         </menu>
 *       </form>
 *     </dialog>
 *   </timlee-dialog>
 *
 *   <a href="/keys/42/revoke" data-dialog-open="revoke">Revoke</a>
 *
 * WHAT IT IS FOR
 * Everything hard about a modal — the focus trap, making the rest of the page
 * inert, Escape, the top layer, focus restoration — is the browser's job via
 * `showModal()`. Hand-rolling any of it is how dialogs end up subtly
 * inaccessible, so this element does none of it.
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM. `aria-labelledby` must reach the heading, `<form
 *    method="dialog">` must see its buttons, and `showModal()` must be called
 *    on an element the page can also address by id.
 *  - The trigger is a real link to a real server-rendered page. With no
 *    JavaScript it navigates and the Go handler renders a full confirmation
 *    page; with JavaScript the navigation is cancelled and the dialog opens.
 *    That is the only arrangement where the destructive action stays
 *    reachable when the bundle fails to load.
 *  - Light dismiss uses the `closedby` attribute (Chrome/Edge 134+) when the
 *    browser supports it, and a backdrop-geometry click check otherwise. The
 *    geometry check is used rather than `event.target === dialog` because the
 *    latter also fires for clicks on padding inside the dialog.
 *  - The page behind does not scroll because the dialog and its backdrop
 *    contain scroll gestures (`overscroll-behavior` in dialog.css). The root's
 *    overflow is never touched: hiding it would take the scrollbar away and
 *    shift the page sideways. WebKit does not contain gestures over a
 *    backdrop yet; there the page can scroll behind the dialog, which stays
 *    modal regardless.
 *  - Focus goes back where it came from because the browser returns it, on
 *    close, to whatever had it when `showModal()` ran. Safari does not focus a
 *    link or button on click, so `show(opener)` focuses the opener first.
 */

import { t } from '../assets/ts/i18n';
import { define, focusableWithin, reflectBool, TimleeElement } from './base';

/** `closedby` is Chrome/Edge 134+; everything else gets the fallback below. */
const SUPPORTS_CLOSEDBY =
  typeof HTMLDialogElement !== 'undefined' && 'closedBy' in HTMLDialogElement.prototype;

export class TimleeDialog extends TimleeElement {
  get dialog(): HTMLDialogElement | null {
    return this.querySelector<HTMLDialogElement>(':scope > dialog');
  }

  get open(): boolean {
    return this.dialog?.open ?? false;
  }

  /**
   * The `closedby` keyword for the native dialog: `closerequest` (the
   * default: Escape only), `any` (light dismiss too) or `none`. The
   * `closed-by` attribute also accepts `close-request` for the default.
   */
  get closedBy(): 'none' | 'closerequest' | 'any' {
    const value = this.getAttribute('closed-by');
    return value === 'any' || value === 'none' ? value : 'closerequest';
  }

  protected override hydrate(): void {
    const dialog = this.dialog;
    if (!dialog) return;

    if (SUPPORTS_CLOSEDBY) {
      (dialog as HTMLDialogElement & { closedBy: string }).closedBy = this.closedBy;
    }

    if (this.hasAttribute('dismissible') && !this.querySelector('.timlee-dialog__close')) {
      const close = document.createElement('button');
      close.type = 'button';
      close.className = 'timlee-dialog__close';
      close.setAttribute(
        'aria-label',
        this.getAttribute('close-label') ?? t('components.dialog.close'),
      );
      close.textContent = '\u00d7';
      dialog.prepend(close);
    }

    // A server-rendered `<dialog open>` is a non-modal inline panel. Promote
    // it to a real modal now that script can, so the markup degrades to
    // something visible and upgrades to something trapped. The `close` event
    // this queues arrives after the dialog is open again; the listener in
    // connected() ignores it.
    if (dialog.open && !dialog.matches(':modal')) {
      dialog.close();
      this.show();
    }
    reflectBool(this, 'open', dialog.open);
  }

  protected override connected(signal: AbortSignal): void {
    const dialog = this.dialog;
    if (!dialog) return;

    this.querySelector('.timlee-dialog__close')?.addEventListener(
      'click',
      () => this.close('dismiss'),
      { signal },
    );

    dialog.addEventListener(
      'close',
      () => {
        // A close that was queued before the dialog was opened again.
        if (dialog.open) return;
        reflectBool(this, 'open', false);
        this.emit('timlee-close', { returnValue: dialog.returnValue });
      },
      { signal },
    );

    // Escape is suppressed only when the author asked for a dialog that must
    // be answered; the buttons inside remain the way out. Browsers with
    // `closedby` enforce this themselves, so the listener is the fallback.
    if (!SUPPORTS_CLOSEDBY) {
      dialog.addEventListener(
        'cancel',
        (event) => {
          if (this.closedBy === 'none') event.preventDefault();
        },
        { signal },
      );
    }

    if (!SUPPORTS_CLOSEDBY && this.closedBy === 'any') {
      dialog.addEventListener('click', this.#backdropClick, { signal });
    }

    // Triggers live anywhere in the document, so they are delegated.
    document.addEventListener('click', this.#delegatedTrigger, { signal });
  }

  #delegatedTrigger = (event: MouseEvent): void => {
    const trigger = (event.target as Element | null)?.closest<HTMLElement>('[data-dialog-open]');
    if (!trigger || trigger.dataset.dialogOpen !== this.id) return;
    // The link still works with no JavaScript; here the dialog replaces it.
    event.preventDefault();
    this.show(trigger);
  };

  #backdropClick = (event: MouseEvent): void => {
    const dialog = this.dialog;
    if (!dialog || event.target !== dialog) return;
    // `event.target === dialog` is also true for clicks on the dialog's own
    // padding, so the point is tested against its box.
    const box = dialog.getBoundingClientRect();
    const inside =
      event.clientX >= box.left &&
      event.clientX <= box.right &&
      event.clientY >= box.top &&
      event.clientY <= box.bottom;
    if (!inside) this.close('dismiss');
  };

  /**
   * Open as a modal. On close the browser returns focus to whatever had it
   * when the dialog opened; pass `opener` when that should be the control
   * that opened it but might not hold focus, as after a click in Safari.
   */
  show(opener?: HTMLElement | null): void {
    const dialog = this.dialog;
    if (!dialog || dialog.open) return;
    if (!this.emit('timlee-open')) return;

    if (opener?.isConnected && opener !== document.activeElement) {
      opener.focus({ preventScroll: true });
    }
    dialog.showModal();
    reflectBool(this, 'open', true);
    this.#focusInitial(dialog);
  }

  close(returnValue?: string): void {
    const dialog = this.dialog;
    if (!dialog?.open) return;
    dialog.close(returnValue ?? '');
  }

  #focusInitial(dialog: HTMLDialogElement): void {
    // showModal() already focuses something sensible; this only overrides it
    // when the author marked a better target, and avoids landing on a
    // destructive button by default.
    const preferred =
      dialog.querySelector<HTMLElement>('[autofocus]') ??
      dialog.querySelector<HTMLElement>('[data-dialog-initial-focus]');
    if (preferred) {
      preferred.focus();
      return;
    }
    if (dialog.contains(document.activeElement)) return;
    (focusableWithin(dialog)[0] ?? dialog).focus();
  }
}

define('timlee-dialog', TimleeDialog);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-dialog': TimleeDialog;
  }
}
