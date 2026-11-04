import { afterEach, describe, expect, it, vi } from 'vitest';
import { $, cleanup, mount, moveAround, record, settle } from './test-utils';
import type { TimleeToast, TimleeToaster } from './toast';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

/** What the page's polite announcer last said. */
function spoken(): string {
  return document.querySelector('[data-timlee-live="polite"]')?.textContent ?? '';
}

describe('<timlee-toast>', () => {
  it('is spoken once by the page\u2019s polite announcer, not as a region of its own', async () => {
    const el = $<TimleeToast>(
      mount(
        '<timlee-toaster><timlee-toast tone="success">Key <b>created</b>.</timlee-toast></timlee-toaster>',
      ),
      'timlee-toast',
    );
    await settle();

    // An inserted role="status" with its text already inside is announced
    // unreliably, VoiceOver above all; the announcer exists beforehand.
    expect(el.hasAttribute('role')).toBe(false);
    // Exactly the message: not the dismiss button's name with it.
    expect(spoken()).toBe('Key created.');
  });

  it('interrupts for an error', () => {
    const el = $<TimleeToast>(
      mount('<timlee-toast tone="danger">Upstream timeout.</timlee-toast>'),
      'timlee-toast',
    );
    expect(el.getAttribute('role')).toBe('alert');
    // An error does not evaporate while it is being read.
    expect(el.duration).toBe(0);
  });

  it('adds a labelled dismiss button and removes itself', () => {
    const container = mount('<timlee-toast>Saved.</timlee-toast>');
    const el = $<TimleeToast>(container, 'timlee-toast');
    const button = $<HTMLButtonElement>(container, '.timlee-toast__dismiss');

    expect(button.getAttribute('aria-label')).toBe('Dismiss notification');
    button.click();
    expect(el.isConnected).toBe(false);
  });

  it('lets a listener veto the removal', () => {
    const container = mount('<timlee-toast>Saved.</timlee-toast>');
    const el = $<TimleeToast>(container, 'timlee-toast');
    el.addEventListener('timlee-toast-dismiss', (event) => event.preventDefault());

    $<HTMLButtonElement>(container, '.timlee-toast__dismiss').click();
    expect(el.isConnected).toBe(true);
  });

  it('reports the dismissal reason', () => {
    const container = mount('<timlee-toast>Saved.</timlee-toast>');
    const el = $<TimleeToast>(container, 'timlee-toast');
    const events = record<CustomEvent<{ reason: string }>>(el, 'timlee-toast-dismiss');

    $<HTMLButtonElement>(container, '.timlee-toast__dismiss').click();
    expect(events()[0]?.detail.reason).toBe('user');
  });

  it('auto-dismisses after its duration', async () => {
    vi.useFakeTimers();
    const container = mount('<timlee-toast duration="1000">Saved.</timlee-toast>');
    const el = $<TimleeToast>(container, 'timlee-toast');

    vi.advanceTimersByTime(999);
    expect(el.isConnected).toBe(true);

    vi.advanceTimersByTime(2);
    expect(el.isConnected).toBe(false);
  });

  it('pauses the countdown while the pointer is over it (WCAG 2.2.1)', () => {
    vi.useFakeTimers();
    const container = mount('<timlee-toast duration="1000">Saved.</timlee-toast>');
    const el = $<TimleeToast>(container, 'timlee-toast');

    vi.advanceTimersByTime(500);
    el.dispatchEvent(new PointerEvent('pointerenter'));
    vi.advanceTimersByTime(5000);
    expect(el.isConnected).toBe(true);

    el.dispatchEvent(new PointerEvent('pointerleave'));
    vi.advanceTimersByTime(600);
    expect(el.isConnected).toBe(false);
  });

  it('pauses while something inside it has focus', () => {
    vi.useFakeTimers();
    const container = mount(
      '<timlee-toast duration="1000">Saved. <a href="/keys">View</a></timlee-toast>',
    );
    const el = $<TimleeToast>(container, 'timlee-toast');

    el.dispatchEvent(new FocusEvent('focusin', { bubbles: true }));
    vi.advanceTimersByTime(5000);
    expect(el.isConnected).toBe(true);
  });

  it('never auto-dismisses an alert', () => {
    vi.useFakeTimers();
    const container = mount('<timlee-toast tone="danger">Boom.</timlee-toast>');
    const el = $<TimleeToast>(container, 'timlee-toast');
    vi.advanceTimersByTime(60_000);
    expect(el.isConnected).toBe(true);
  });

  it('omits the dismiss button when told to', () => {
    const container = mount('<timlee-toast no-dismiss>Saved.</timlee-toast>');
    expect(container.querySelector('.timlee-toast__dismiss')).toBeNull();
  });

  it('dismisses exactly once from its button after being moved', () => {
    const container = mount('<timlee-toast>Saved.</timlee-toast>');
    const el = $<TimleeToast>(container, 'timlee-toast');
    const events = record(el, 'timlee-toast-dismiss');
    el.addEventListener('timlee-toast-dismiss', (event) => event.preventDefault());
    moveAround(el);

    $<HTMLButtonElement>(container, '.timlee-toast__dismiss').click();
    expect(events()).toHaveLength(1);
  });
});

describe('<timlee-toaster>', () => {
  it('is a labelled region so it can be found again', () => {
    const el = $<TimleeToaster>(mount('<timlee-toaster></timlee-toaster>'), 'timlee-toaster');
    expect(el.getAttribute('role')).toBe('region');
    expect(el.getAttribute('aria-label')).toBe('Notifications');
  });

  it('appends toasts through the imperative API', async () => {
    const el = $<TimleeToaster>(mount('<timlee-toaster></timlee-toaster>'), 'timlee-toaster');
    const toast = el.show('Rotated.', { tone: 'success', duration: 0 });
    await settle();

    expect(toast.parentElement).toBe(el);
    expect(toast.getAttribute('tone')).toBe('success');
    expect(el.textContent).toContain('Rotated.');
    expect(spoken()).toBe('Rotated.');
  });

  it('holds the top layer while it has toasts, above menus and drawers', async () => {
    const el = $<TimleeToaster>(mount('<timlee-toaster></timlee-toaster>'), 'timlee-toaster');
    expect(el.getAttribute('popover')).toBe('manual');
    expect(el.matches(':popover-open')).toBe(false);

    const toast = el.show('Saved.', { duration: 0 });
    await settle();
    expect(el.matches(':popover-open')).toBe(true);

    toast.dismiss();
    await settle();
    expect(el.matches(':popover-open')).toBe(false);
    expect(getComputedStyle(el).display).toBe('none');
  });

  it('stays in the page when asked to', async () => {
    const el = $<TimleeToaster>(
      mount('<timlee-toaster inline></timlee-toaster>'),
      'timlee-toaster',
    );
    el.show('Saved.', { duration: 0 });
    await settle();
    expect(el.hasAttribute('popover')).toBe(false);
    expect(getComputedStyle(el).position).toBe('static');
  });

  it('waits for an open modal to close before it shows or speaks', async () => {
    const container = mount(`
      <timlee-toaster></timlee-toaster>
      <dialog><form method="dialog"><button>Close</button></form></dialog>
    `);
    const el = $<TimleeToaster>(container, 'timlee-toaster');
    const dialog = $<HTMLDialogElement>(container, 'dialog');
    dialog.showModal();

    const saved = el.show('Preferences saved.', { tone: 'success', duration: 0 });
    const failed = el.show('Timlee could not be reached.', { tone: 'danger' });
    await settle();
    // Behind the modal the page is inert: nothing raised, said or alerted.
    expect(el.matches(':popover-open')).toBe(false);
    expect(spoken()).not.toContain('Preferences saved.');
    expect(failed.hasAttribute('role')).toBe(false);

    const closed = new Promise((resolve) => dialog.addEventListener('close', resolve));
    dialog.close();
    await closed;
    await settle();

    expect(el.matches(':popover-open')).toBe(true);
    expect(spoken()).toBe('Preferences saved.');
    expect(failed.getAttribute('role')).toBe('alert');
    expect(saved.isConnected).toBe(true);
  });

  it('lets toasts go when the modal they waited for leaves the page unclosed', async () => {
    const container = mount(`
      <timlee-toaster></timlee-toaster>
      <div data-swap><dialog><p>Remove this member?</p></dialog></div>
    `);
    const el = $<TimleeToaster>(container, 'timlee-toaster');
    const dialog = $<HTMLDialogElement>(container, 'dialog');
    dialog.showModal();
    el.show('Member removed.', { tone: 'success', duration: 0 });
    await settle();
    expect(el.matches(':popover-open')).toBe(false);

    // settings.ts replaces the page's content, open dialog and all, and a
    // removed dialog never fires close.
    $(container, '[data-swap]').replaceChildren();
    await settle();

    expect(el.matches(':popover-open')).toBe(true);
    expect(spoken()).toBe('Member removed.');
  });

  it('reuses the page toaster rather than stacking new ones', () => {
    const el = $<TimleeToaster>(mount('<timlee-toaster></timlee-toaster>'), 'timlee-toaster');
    expect(customElements.get('timlee-toaster')).toBeTruthy();
    const found = (customElements.get('timlee-toaster') as typeof TimleeToaster).current();
    expect(found).toBe(el);
  });
});
