/**
 * announce.ts — the page's live regions, and speaking through them.
 *
 * Its own module, apart from a11y.ts, because a page may load it with the
 * toasts on every page view, and the keyboard patterns there would only be
 * dead weight in that bundle.
 *
 * While a modal dialog is open the rest of the page is inert, live regions
 * included, so nothing said there is read. The top layer is no way out: a
 * popover shown over the dialog is inert too. So the regions themselves are
 * lent to the open dialog when there is something to say, and given back
 * when it closes: still one polite and one assertive region on the page.
 */

import { whenParsed } from './base';

type Politeness = 'polite' | 'assertive';

const regions: Partial<Record<Politeness, HTMLElement>> = {};

/** Where each region lives in the page, to go back to after a dialog. */
const homes = new WeakMap<HTMLElement, { parent: ParentNode; next: ChildNode | null }>();

/** The regions currently lent to an open modal dialog. */
const lent = new WeakSet<HTMLElement>();

/**
 * When each region last moved. A moved region is new to assistive
 * technology, which reads only changes to regions it already knows, so it
 * waits this long before it speaks.
 */
const movedAt = new WeakMap<HTMLElement, number>();
const SETTLE_MS = 150;

/** The latest message for each region: only it is written. */
const latest = new WeakMap<HTMLElement, number>();

/**
 * The page's live region for `politeness`. A layout that renders its own
 * announcer has it adopted:
 *
 *   <p class="visually-hidden" role="status" aria-live="polite" aria-atomic="true" data-announcer></p>
 *
 * (`data-announcer="assertive"` with `role="alert"` for the other). Otherwise
 * one is made here. Either way the page has one polite and one assertive
 * region, however many components speak.
 */
function liveRegion(politeness: Politeness): HTMLElement {
  const known = regions[politeness];
  // Lent to a dialog that was then removed while open, it went with it.
  if (known && !known.isConnected && lent.has(known)) goHome(known);
  if (known?.isConnected) return known;

  let region = document.querySelector<HTMLElement>(
    politeness === 'polite'
      ? '[data-announcer]:not([data-announcer="assertive"])'
      : '[data-announcer="assertive"]',
  );
  if (!region) {
    region = document.createElement('div');
    region.className = 'timlee-sr-only';
    region.setAttribute('role', politeness === 'polite' ? 'status' : 'alert');
    region.setAttribute('aria-live', politeness);
    region.setAttribute('aria-atomic', 'true');
    region.setAttribute('data-timlee-live', politeness);
    document.body.append(region);
  }

  regions[politeness] = region;
  if (region.parentNode) homes.set(region, { parent: region.parentNode, next: region.nextSibling });
  return region;
}

/** The modal dialog that holds focus, which is the topmost one. */
function openModal(): HTMLDialogElement | null {
  return (
    document.activeElement?.closest<HTMLDialogElement>('dialog:modal') ??
    document.querySelector<HTMLDialogElement>('dialog:modal')
  );
}

function move(region: HTMLElement, parent: ParentNode, before: ChildNode | null): void {
  parent.insertBefore(region, before);
  movedAt.set(region, performance.now());
}

function goHome(region: HTMLElement): void {
  lent.delete(region);
  const home = homes.get(region);
  if (!home?.parent.isConnected || region.parentNode === home.parent) return;
  move(region, home.parent, home.next?.parentNode === home.parent ? home.next : null);
}

/** Put `region` where it can be heard: in the open modal, or back home. */
function place(region: HTMLElement): void {
  const modal = openModal();
  if (!modal) {
    if (lent.has(region)) goHome(region);
    return;
  }
  if (modal.contains(region)) return;
  move(region, modal, null);
  lent.add(region);
  // Back home when it closes, unless another modal still needs it.
  modal.addEventListener('close', () => place(region), { once: true });
}

// Made as soon as the module loads: a region that appears in the same moment
// as its first message is not reliably announced, which lost the first
// "Copied" on every page.
whenParsed(() => {
  liveRegion('polite');
  liveRegion('assertive');
});

/**
 * Speak a transient message — "Copied", "3 of 12 selected".
 *
 * The region is emptied first because repeating identical text into a live
 * region is, in several screen readers, not announced at all.
 */
export function announce(message: string, politeness: Politeness = 'polite'): void {
  const region = liveRegion(politeness);
  place(region);
  region.textContent = '';

  const id = (latest.get(region) ?? 0) + 1;
  latest.set(region, id);
  const speak = () => {
    if (latest.get(region) === id) region.textContent = message;
  };
  const wait = (movedAt.get(region) ?? Number.NEGATIVE_INFINITY) + SETTLE_MS - performance.now();
  if (wait > 0) setTimeout(speak, wait);
  else requestAnimationFrame(speak);
}

/** Test seam: forget the regions, removing the ones made here. */
export function resetLiveRegions(): void {
  for (const region of Object.values(regions)) {
    if (region?.hasAttribute('data-timlee-live')) region.remove();
  }
  delete regions.polite;
  delete regions.assertive;
}
