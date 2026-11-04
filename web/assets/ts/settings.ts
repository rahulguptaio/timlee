/**
 * settings.ts — enhancement for the account and organisation pages.
 *
 * Every form on these pages already works without this file: it posts, the
 * server answers with a redirect, and the next page carries the outcome. This
 * module only removes the page load. It sends the same form with fetch, lets
 * the browser follow the same redirect, and swaps the returned page's content
 * in place — so the server still renders every state, including validation
 * errors and role changes, and nothing here decides what anyone may do.
 *
 * On top of that it adds the parts that need a script: the outcome as a toast,
 * focus put somewhere sensible after the swap, the inline confirmation for
 * revoking a key without a round trip, and a guard that keeps a new key's
 * secret on screen until its owner says they have stored it.
 */

// The busy state of <timlee-button loading>, and live roles for the alerts a
// swap brings in, need their modules whatever the first page held.
import '../../components/alert';
import '../../components/button';
import { loadComponents } from '../../components/loader';
import { TimleeToaster, type ToastTone } from '../../components/toast';
import { t } from './i18n';

const TONES: ReadonlySet<string> = new Set(['success', 'info', 'warning', 'danger']);

let busy = false;
let guarding = false;

export function enhanceSettings(): void {
  document.addEventListener('submit', onSubmit);
  document.addEventListener('click', onClick);
  if (document.querySelector('[data-vault]')) guardSecret(true);
}

/* --- Forms ---------------------------------------------------------------- */

function onSubmit(event: SubmitEvent): void {
  const form = event.target as HTMLFormElement;
  const submitter = event.submitter as HTMLButtonElement | HTMLInputElement | null;
  // A dialog's own Cancel closes it natively; there is nothing to send.
  if (submitter?.getAttribute('formmethod') === 'dialog') return;

  if (!form.hasAttribute('data-enhance')) return;

  event.preventDefault();
  if (busy) return;
  void send(form, submitter);
}

async function send(form: HTMLFormElement, submitter: HTMLElement | null): Promise<void> {
  busy = true;
  const wrapper = submitter?.closest('timlee-button');
  wrapper?.setAttribute('loading', '');
  const returnFocus = form.closest('section')?.querySelector('h2')?.id ?? '';

  try {
    // URL-encoded, as the browser would send it: the handlers read a plain
    // form body, not multipart.
    const body = new URLSearchParams();
    for (const [name, value] of new FormData(form, submitter)) {
      if (typeof value === 'string') body.append(name, value);
    }
    const response = await fetch(form.action, {
      method: 'POST',
      body,
      credentials: 'same-origin',
      headers: { Accept: 'text/html' },
    });
    const page = parse(await response.text());

    const swapped = swap(page);
    if (!swapped) {
      // Not a settings page — a sign-in redirect, an outage or a refused
      // form. Show it as the browser would have.
      if (response.redirected) {
        window.location.assign(response.url);
        return;
      }
      document.title = page.querySelector('title')?.textContent ?? document.title;
      document.body.replaceChildren(
        ...[...page.children].filter(
          (el) => !['META', 'TITLE', 'LINK', 'STYLE', 'SCRIPT'].includes(el.tagName),
        ),
      );
      return;
    }
    if (response.redirected) history.replaceState(null, '', response.url);
    settle(response.status, swapped.fresh, returnFocus);
  } catch {
    toast(t('errors.settings.network'), 'danger');
  } finally {
    wrapper?.removeAttribute('loading');
    busy = false;
  }
}

/**
 * Parse a response inertly. A template's content belongs to a document with no
 * browsing context, so the page's own nonce-carrying <style> is never checked
 * against this page's policy — which DOMParser would do, and report.
 */
function parse(html: string): DocumentFragment {
  const template = document.createElement('template');
  template.innerHTML = html;
  return template.content;
}

/**
 * Replace this page's content with the server's new rendering of it. The
 * outcome message comes out of the new content and becomes a toast, and a
 * secret still on screen is carried across: the server shows it once, so this
 * page is the only place it still exists. Reports the secret the server just
 * revealed, if it revealed one.
 */
function swap(page: DocumentFragment): { fresh: HTMLElement | null } | null {
  const main = document.querySelector('[data-st-main]');
  const next = page.querySelector('[data-st-main]');
  if (!main || !next) return null;

  const flash = next.querySelector<HTMLElement>('[data-flash]');
  flash?.remove();

  const fresh = next.querySelector<HTMLElement>('[data-vault]');
  const kept = [...main.querySelectorAll('[data-vault]')];
  if (kept.length) (fresh ?? next.querySelector('.st-head'))?.after(...kept);

  main.replaceChildren(...next.childNodes);
  void loadComponents(main);
  const rail = document.querySelector('[data-st-rail]');
  const nextRail = page.querySelector('[data-st-rail]');
  if (rail && nextRail) {
    rail.replaceChildren(...nextRail.childNodes);
    void loadComponents(rail);
  }
  document.title = page.querySelector('title')?.textContent ?? document.title;

  const tone = flash?.dataset.flash ?? '';
  // The region's heading names the landmark; the toast says only the outcome.
  const message = flash?.querySelector('p')?.textContent?.trim();
  if (message) toast(message, TONES.has(tone) ? (tone as ToastTone) : 'info');
  return { fresh };
}

/** Put focus where the reader's next decision is. */
function settle(status: number, fresh: HTMLElement | null, returnFocus: string): void {
  const main = document.querySelector<HTMLElement>('[data-st-main]');
  if (!main) return;
  guardSecret(Boolean(main.querySelector('[data-vault]')));

  // The error summary first, as on a page load, where autofocus puts it: it
  // says how many things need fixing and links to each.
  const problem =
    main.querySelector<HTMLElement>('[data-error-summary]') ??
    main.querySelector<HTMLElement>('[aria-invalid="true"]');
  if (status === 422 && problem) {
    problem.focus();
    return;
  }
  if (fresh) {
    fresh.focus();
    return;
  }
  const heading = returnFocus ? document.getElementById(returnFocus) : null;
  const target = heading ?? main;
  if (!target.hasAttribute('tabindex')) target.setAttribute('tabindex', '-1');
  target.focus();
}

function toast(message: string, tone: ToastTone): void {
  TimleeToaster.current().show(message, { tone });
}

/* --- Inline confirmation and the new key ---------------------------------------- */

function onClick(event: MouseEvent): void {
  const target = event.target as Element | null;

  const open = target?.closest<HTMLAnchorElement>('a[data-inline-confirm]');
  if (open) {
    const row = document.getElementById(open.dataset.inlineConfirm ?? '');
    if (!row) return;
    event.preventDefault();
    row.hidden = false;
    open.setAttribute('aria-expanded', 'true');
    row.querySelector<HTMLElement>('p[tabindex]')?.focus();
    return;
  }

  const cancel = target?.closest<HTMLAnchorElement>('a[data-inline-cancel]');
  if (cancel) {
    const row = document.getElementById(cancel.dataset.inlineCancel ?? '');
    if (!row) return;
    event.preventDefault();
    row.hidden = true;
    const trigger = document.querySelector<HTMLElement>(`[data-inline-confirm="${row.id}"]`);
    trigger?.setAttribute('aria-expanded', 'false');
    trigger?.focus();
    return;
  }

  const done = target?.closest<HTMLAnchorElement>('a[data-vault-done]');
  if (done) {
    event.preventDefault();
    done.closest('[data-vault]')?.remove();
    const another = document.querySelector<HTMLElement>('[data-vault]');
    guardSecret(Boolean(another));
    (another ?? document.getElementById('keys-list'))?.focus();
  }
}

/** Ask before leaving while a secret that cannot be shown again is on screen. */
function guardSecret(on: boolean): void {
  if (on === guarding) return;
  guarding = on;
  if (on) window.addEventListener('beforeunload', holdSecret);
  else window.removeEventListener('beforeunload', holdSecret);
}

function holdSecret(event: BeforeUnloadEvent): void {
  event.preventDefault();
}
