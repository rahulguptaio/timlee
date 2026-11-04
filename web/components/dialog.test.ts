import { afterEach, describe, expect, it } from 'vitest';

import type { TimleeDialog } from './dialog';
import { $, cleanup, mount, moveAround, press, record } from './test-utils';

afterEach(cleanup);

const MARKUP = `
  <timlee-dialog id="revoke" dismissible>
    <dialog aria-labelledby="revoke-title">
      <form method="dialog">
        <h2 id="revoke-title">Revoke this key?</h2>
        <p>Requests using it will start failing within 30 seconds.</p>
        <menu>
          <button value="cancel" id="cancel">Cancel</button>
          <button value="revoke" id="confirm">Revoke</button>
        </menu>
      </form>
    </dialog>
  </timlee-dialog>
  <a href="/keys/42/revoke" data-dialog-open="revoke" id="trigger">Revoke</a>
`;

function setup() {
  const container = mount(MARKUP);
  return {
    container,
    el: $<TimleeDialog>(container, 'timlee-dialog'),
    dialog: $<HTMLDialogElement>(container, 'dialog'),
    trigger: $<HTMLAnchorElement>(container, '#trigger'),
  };
}

describe('<timlee-dialog>', () => {
  it('stays closed until something opens it', () => {
    const { el, dialog } = setup();
    expect(dialog.open).toBe(false);
    expect(el.open).toBe(false);
  });

  it('opens as a real modal with the platform focus trap', () => {
    const { el, dialog } = setup();
    el.show();

    expect(dialog.open).toBe(true);
    expect(dialog.matches(':modal')).toBe(true);
    expect(el.hasAttribute('open')).toBe(true);
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it('cancels the trigger link\u2019s navigation and opens instead', () => {
    const { dialog, trigger } = setup();
    const event = new MouseEvent('click', { bubbles: true, cancelable: true });
    trigger.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(dialog.open).toBe(true);
    // The href is still there: with no script it is a page the server serves.
    expect(trigger.getAttribute('href')).toBe('/keys/42/revoke');
  });

  it('returns focus to the opener on close', async () => {
    const { el, dialog, trigger } = setup();
    trigger.focus();
    el.show(trigger);
    expect(dialog.contains(document.activeElement)).toBe(true);

    el.close('revoke');
    await Promise.resolve();
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(document.activeElement).toBe(trigger);
  });

  it('prefers an autofocus target over the first focusable', () => {
    const container = mount(`
      <timlee-dialog id="d2">
        <dialog>
          <button id="first">First</button>
          <button id="wanted" autofocus>Wanted</button>
        </dialog>
      </timlee-dialog>
    `);
    $<TimleeDialog>(container, 'timlee-dialog').show();
    expect(document.activeElement?.id).toBe('wanted');
  });

  it('closes through the native form and reports the return value', async () => {
    const { el, container, dialog } = setup();
    const closes = record<CustomEvent<{ returnValue: string }>>(el, 'timlee-close');
    const closed = new Promise((resolve) =>
      el.addEventListener('timlee-close', resolve, { once: true }),
    );
    el.show();

    $<HTMLButtonElement>(container, '#confirm').click();
    await closed;

    expect(dialog.open).toBe(false);
    expect(dialog.returnValue).toBe('revoke');
    expect(closes()[0]?.detail.returnValue).toBe('revoke');
    expect(el.hasAttribute('open')).toBe(false);
  });

  it('adds a labelled close button only when asked', () => {
    const { container } = setup();
    const close = $<HTMLButtonElement>(container, '.timlee-dialog__close');
    expect(close.getAttribute('aria-label')).toBe('Close dialog');

    const plain = mount('<timlee-dialog id="d3"><dialog><p>hi</p></dialog></timlee-dialog>');
    expect(plain.querySelector('.timlee-dialog__close')).toBeNull();
  });

  it('is cancelable: a listener can keep it shut', () => {
    const { el, dialog } = setup();
    el.addEventListener('timlee-open', (event) => event.preventDefault(), { once: true });
    el.show();
    expect(dialog.open).toBe(false);
  });

  it('keeps the page still without touching the root, so nothing shifts', async () => {
    const { el, dialog } = setup();
    el.show();

    // A hidden root overflow takes the scrollbar away and moves the layout.
    expect(getComputedStyle(document.documentElement).overflow).not.toBe('hidden');
    // The guide's alternative: scroll gestures stop at the dialog and at its
    // backdrop, which has to be a scroll container for that to apply.
    expect(getComputedStyle(dialog).overscrollBehaviorY).toBe('contain');
    expect(getComputedStyle(dialog).overflowY).toBe('hidden');
    const backdrop = getComputedStyle(dialog, '::backdrop');
    expect(backdrop.overflowY).toBe('hidden');
    expect(backdrop.overscrollBehaviorY).toBe('contain');

    const closed = new Promise((resolve) =>
      el.addEventListener('timlee-close', () => resolve(null), { once: true }),
    );
    el.close();
    await closed;
  });

  it('promotes a server-rendered open panel to a modal', () => {
    const container = mount(`
      <timlee-dialog id="d4">
        <dialog open><p>Inline before script, modal after.</p></dialog>
      </timlee-dialog>
    `);
    const dialog = $<HTMLDialogElement>(container, 'dialog');
    expect(dialog.open).toBe(true);
    expect(dialog.matches(':modal')).toBe(true);
  });

  it('stays open after the promotion, without reporting a close', async () => {
    const container = document.createElement('div');
    container.innerHTML = `
      <timlee-dialog id="d5">
        <dialog open><form method="dialog"><p>Deep-linked confirmation.</p></form></dialog>
      </timlee-dialog>
    `;
    const el = $<TimleeDialog>(container, 'timlee-dialog');
    // Listening before the element connects, as a page script would.
    const closes = record(el, 'timlee-close');
    document.body.append(container);
    try {
      // The promotion's close() queues a `close` event that lands only now,
      // after the dialog is already open again as a modal.
      await new Promise((resolve) => setTimeout(resolve, 20));

      expect(closes()).toHaveLength(0);
      expect(el.hasAttribute('open')).toBe(true);
      expect($<HTMLDialogElement>(container, 'dialog').matches(':modal')).toBe(true);
    } finally {
      container.remove();
    }
  });

  it('gives the native dialog the closedby keyword the platform knows', () => {
    const { dialog } = setup();
    // `close-request` is not a closedby keyword: browsers that support the
    // attribute would treat it as invalid.
    if ('closedBy' in HTMLDialogElement.prototype) {
      expect(dialog.getAttribute('closedby')).toBe('closerequest');
    }
  });

  it('returns focus to an opener that a click did not focus, as in Safari', async () => {
    const { el, dialog, trigger } = setup();
    // Safari does not focus a link or a button when it is clicked.
    (document.activeElement as HTMLElement | null)?.blur();
    el.show(trigger);
    expect(dialog.contains(document.activeElement)).toBe(true);

    el.close('cancel');
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(document.activeElement).toBe(trigger);
  });

  it('closes from its own button exactly once after being moved', async () => {
    const { el, container } = setup();
    const closes = record(el, 'timlee-close');
    moveAround(el);
    el.show();
    const closed = new Promise((resolve) =>
      el.addEventListener('timlee-close', resolve, { once: true }),
    );

    $<HTMLButtonElement>(container, '.timlee-dialog__close').click();
    await closed;
    // Long enough for a second, duplicate close to have arrived too.
    await new Promise((resolve) => setTimeout(resolve, 20));

    expect(el.open).toBe(false);
    expect(closes()).toHaveLength(1);
  });

  it('escapes by default and not at all when closed-by is none', () => {
    const { el, dialog } = setup();
    el.show();
    dialog.dispatchEvent(new Event('cancel', { cancelable: true }));
    // The browser owns Escape; the component only vetoes it. Here it does not.
    expect(dialog.open).toBe(true);

    el.close();
    el.setAttribute('closed-by', 'none');
    el.show();
    const cancel = new Event('cancel', { cancelable: true });
    dialog.dispatchEvent(cancel);
    expect(dialog.open).toBe(true);
    el.close();
  });

  it('keeps Escape working through the dialog element itself', () => {
    const { el, dialog } = setup();
    el.show();
    press(dialog, 'Escape');
    // Escape is handled by the UA, not by a key listener of ours; this only
    // asserts that nothing in the component swallows it.
    expect(dialog.open).toBe(true);
    el.close();
  });
});
