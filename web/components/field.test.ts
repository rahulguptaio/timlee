import { afterEach, describe, expect, it } from 'vitest';

import type { TimleeInput } from './input';
import type { TimleeSelect } from './select';
import { $, cleanup, mount, moveAround, settle } from './test-utils';
import type { TimleeTextarea } from './textarea';

afterEach(cleanup);

const FIELD = `
  <timlee-input>
    <label>Work email</label>
    <input name="email" type="email" required>
    <p data-hint>Billing receipts go here.</p>
    <p data-error></p>
  </timlee-input>
`;

describe('<timlee-input>', () => {
  it('associates a label that has no for attribute', () => {
    const el = $<TimleeInput>(mount(FIELD), 'timlee-input');
    const control = el.control as HTMLInputElement;
    const label = el.label as HTMLLabelElement;

    expect(control.id).toBeTruthy();
    expect(label.getAttribute('for')).toBe(control.id);
    expect(label.control).toBe(control);
  });

  it('describes the control with the hint but not an empty error', () => {
    const el = $<TimleeInput>(mount(FIELD), 'timlee-input');
    const control = el.control as HTMLInputElement;
    const described = (control.getAttribute('aria-describedby') ?? '').split(' ');

    expect(described).toContain(el.hintElement?.id);
    expect(described).not.toContain(el.errorElement?.id);
    expect(control.hasAttribute('aria-invalid')).toBe(false);
  });

  it('appends to an aria-describedby the page already set', () => {
    const container = mount(`
      <p id="shared-note">All fields are required.</p>
      <timlee-input>
        <label>Name</label>
        <input aria-describedby="shared-note">
        <p data-hint>Two words.</p>
      </timlee-input>
    `);
    const described = $(container, 'input').getAttribute('aria-describedby') ?? '';
    expect(described.split(' ')).toContain('shared-note');
    expect(described.split(' ').length).toBe(2);
  });

  it('marks itself invalid when the server renders an error, and recovers', async () => {
    const el = $<TimleeInput>(mount(FIELD), 'timlee-input');
    const control = el.control as HTMLInputElement;

    el.error = 'Enter a valid address.';
    await settle();

    expect(el.hasAttribute('invalid')).toBe(true);
    expect(control.getAttribute('aria-invalid')).toBe('true');
    expect((control.getAttribute('aria-describedby') ?? '').split(' ')).toContain(
      el.errorElement?.id,
    );

    el.error = '';
    await settle();
    expect(el.hasAttribute('invalid')).toBe(false);
    expect(control.hasAttribute('aria-invalid')).toBe(false);
  });

  it('notices an error written straight into the DOM, as a Go re-render would', async () => {
    const el = $<TimleeInput>(mount(FIELD), 'timlee-input');
    (el.errorElement as HTMLElement).textContent = 'Already taken.';
    await settle();
    expect(el.hasAttribute('invalid')).toBe(true);
  });

  it('shows the error state once the browser rejects a value, and not before', () => {
    const container = mount(`<form>${FIELD}</form><span id="danger-ink"></span>`);
    const label = $(container, 'label');
    const input = $<HTMLInputElement>(container, 'input');
    const probe = $(container, '#danger-ink');
    probe.style.color = 'var(--ts-danger-ink)';
    const danger = getComputedStyle(probe).color;

    // Empty and required, but untouched: not an error yet.
    expect(getComputedStyle(label).color).not.toBe(danger);

    // A submit attempt is the reader committing to the form's values.
    $<HTMLFormElement>(container, 'form').requestSubmit();
    expect(input.matches(':user-invalid')).toBe(true);
    expect(getComputedStyle(label).color).toBe(danger);

    // Fixing it clears the state straight away.
    input.value = 'ops@timlee.dev';
    expect(getComputedStyle(label).color).not.toBe(danger);
  });

  it('forwards value and native validity', () => {
    const el = $<TimleeInput>(mount(FIELD), 'timlee-input');
    expect(el.checkValidity()).toBe(false);
    el.value = 'ops@timlee.dev';
    expect(el.value).toBe('ops@timlee.dev');
    expect(el.name).toBe('email');
    expect(el.checkValidity()).toBe(true);
  });

  it('submits through the real form', () => {
    const container = mount(`<form>${FIELD}<button type="submit">Go</button></form>`);
    const form = $<HTMLFormElement>(container, 'form');
    $<HTMLInputElement>(container, 'input').value = 'ops@timlee.dev';

    let data: FormData | null = null;
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      data = new FormData(form);
    });
    $<HTMLButtonElement>(container, 'button').click();
    expect((data as unknown as FormData | null)?.get('email')).toBe('ops@timlee.dev');
  });
});

describe('<timlee-textarea>', () => {
  const MARKUP = `
    <timlee-textarea counter autosize>
      <label>Notes</label>
      <textarea name="notes" maxlength="20" rows="2"></textarea>
    </timlee-textarea>
  `;

  it('renders a counter and keeps it quiet until the limit is near', () => {
    const el = $<TimleeTextarea>(mount(MARKUP), 'timlee-textarea');
    const counter = $(el, '.timlee-field__counter');
    const control = el.control as HTMLTextAreaElement;

    expect(counter.textContent).toBe('0 / 20');
    // 20 characters left is already within the announcement window.
    expect(counter.getAttribute('aria-live')).toBe('polite');

    control.value = 'x'.repeat(20);
    control.dispatchEvent(new Event('input', { bubbles: true }));
    expect(counter.textContent).toBe('20 / 20');
    expect(counter.hasAttribute('data-over')).toBe(false);
  });

  it('still counts exactly once per keystroke after it is moved and put back', () => {
    const el = $<TimleeTextarea>(mount(MARKUP), 'timlee-textarea');
    const counter = $(el, '.timlee-field__counter');
    const control = el.control as HTMLTextAreaElement;
    moveAround(el);
    const updates = new MutationObserver(() => {});
    updates.observe(counter, { childList: true });

    control.value = 'abc';
    control.dispatchEvent(new Event('input', { bubbles: true }));

    expect(updates.takeRecords()).toHaveLength(1);
    expect(counter.textContent).toBe('3 / 20');
  });

  it('stays silent while the limit is far away', () => {
    const container = mount(`
      <timlee-textarea counter>
        <label>Notes</label>
        <textarea maxlength="500"></textarea>
      </timlee-textarea>
    `);
    expect($(container, '.timlee-field__counter').getAttribute('aria-live')).toBe('off');
  });

  it('grows with its content when autosize is set', () => {
    const el = $<TimleeTextarea>(mount(MARKUP), 'timlee-textarea');
    const control = el.control as HTMLTextAreaElement;
    const before = control.getBoundingClientRect().height;

    control.value = 'line\n'.repeat(12);
    control.dispatchEvent(new Event('input', { bubbles: true }));

    expect(control.getBoundingClientRect().height).toBeGreaterThan(before);
  });

  it('omits the counter when there is no maxlength to count against', () => {
    const container = mount(
      '<timlee-textarea counter><label>N</label><textarea></textarea></timlee-textarea>',
    );
    expect(container.querySelector('.timlee-field__counter')).toBeNull();
  });
});

describe('<timlee-select>', () => {
  const MARKUP = `
    <timlee-select>
      <label>Environment</label>
      <select name="env">
        <option value="prod">Production</option>
        <option value="staging" selected>Staging</option>
      </select>
    </timlee-select>
  `;

  it('keeps the native select and its selection', () => {
    const el = $<TimleeSelect>(mount(MARKUP), 'timlee-select');
    expect(el.control?.tagName).toBe('SELECT');
    expect(el.value).toBe('staging');
    expect(el.selectedLabel).toBe('Staging');
  });

  it('labels the select', () => {
    const el = $<TimleeSelect>(mount(MARKUP), 'timlee-select');
    expect((el.label as HTMLLabelElement).control).toBe(el.control);
  });

  it('draws its chevron on the host so it exists before upgrade', () => {
    const el = $<TimleeSelect>(mount(MARKUP), 'timlee-select');
    const chevron = getComputedStyle(el, '::after');
    expect(chevron.content).not.toBe('none');
    expect(chevron.backgroundColor).not.toBe('rgba(0, 0, 0, 0)');
  });
});
