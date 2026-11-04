import { afterEach, describe, expect, it } from 'vitest';

import type { TimleeCheckbox } from './choice';
import type { TimleeSwitch } from './switch';
import { $, cleanup, click, mount, press, record } from './test-utils';

afterEach(cleanup);

describe('<timlee-checkbox>', () => {
  const MARKUP = `
    <timlee-checkbox>
      <label><input type="checkbox" name="tos" value="1"><span>I accept</span></label>
      <p data-hint>You can withdraw consent later.</p>
    </timlee-checkbox>
  `;

  it('keeps the native checkbox and its implicit label', () => {
    const el = $<TimleeCheckbox>(mount(MARKUP), 'timlee-checkbox');
    const control = el.control as HTMLInputElement;

    expect(control.type).toBe('checkbox');
    expect(control.labels?.[0]?.textContent).toContain('I accept');
    // An implicit label needs no `for`, and adding one would be noise.
    expect(el.querySelector('label')?.hasAttribute('for')).toBe(false);
  });

  it('describes the control with the hint', () => {
    const el = $<TimleeCheckbox>(mount(MARKUP), 'timlee-checkbox');
    const described = el.control?.getAttribute('aria-describedby');
    expect(described).toBe($(el, '[data-hint]').id);
  });

  it('exposes indeterminate, which has no content attribute natively', () => {
    const el = $<TimleeCheckbox>(mount(MARKUP), 'timlee-checkbox');
    expect(el.control?.indeterminate).toBe(false);

    el.indeterminate = true;
    expect(el.control?.indeterminate).toBe(true);
    expect(el.hasAttribute('indeterminate')).toBe(true);

    el.indeterminate = false;
    expect(el.control?.indeterminate).toBe(false);
  });

  it('honours indeterminate written by the server', () => {
    const container = mount(
      '<timlee-checkbox indeterminate><label><input type="checkbox"><span>All</span></label></timlee-checkbox>',
    );
    expect($<HTMLInputElement>(container, 'input').indeterminate).toBe(true);
  });

  it('toggles through the label and submits its value', () => {
    const container = mount(`<form>${MARKUP}</form>`);
    const form = $<HTMLFormElement>(container, 'form');
    const control = $<HTMLInputElement>(container, 'input');

    expect(new FormData(form).get('tos')).toBeNull();
    $<HTMLLabelElement>(container, 'label').click();
    expect(control.checked).toBe(true);
    expect(new FormData(form).get('tos')).toBe('1');
  });

  it('gives the row a target at least 24px tall', () => {
    const container = mount(MARKUP);
    expect($(container, 'label').getBoundingClientRect().height).toBeGreaterThanOrEqual(24);
  });
});

describe('<timlee-radio>', () => {
  it('is grouped by a fieldset rather than a custom role', () => {
    const container = mount(`
      <fieldset>
        <legend>Deployment</legend>
        <timlee-radio><label><input type="radio" name="mode" value="hosted" checked><span>Hosted</span></label></timlee-radio>
        <timlee-radio><label><input type="radio" name="mode" value="byoc"><span>BYOC</span></label></timlee-radio>
      </fieldset>
    `);

    expect(container.querySelector('[role="radiogroup"]')).toBeNull();
    const radios = container.querySelectorAll<HTMLInputElement>('input[type="radio"]');
    expect(radios).toHaveLength(2);

    radios[1]?.click();
    expect(radios[0]?.checked).toBe(false);
    expect(radios[1]?.checked).toBe(true);
  });
});

describe('<timlee-switch> — native mode', () => {
  const MARKUP = `
    <timlee-switch>
      <label><input type="checkbox" role="switch" name="streaming" checked><span>Stream responses</span></label>
    </timlee-switch>
  `;

  it('leaves the native control in charge', () => {
    const el = $<TimleeSwitch>(mount(MARKUP), 'timlee-switch');
    expect(el.standalone).toBe(false);
    expect(el.checked).toBe(true);
    expect(el.control?.getAttribute('role')).toBe('switch');
    expect(el.hasAttribute('role')).toBe(false);
  });

  it('re-broadcasts the native change as timlee-change', () => {
    const el = $<TimleeSwitch>(mount(MARKUP), 'timlee-switch');
    const events = record<CustomEvent<{ checked: boolean }>>(el, 'timlee-change');

    (el.control as HTMLInputElement).click();

    expect(el.checked).toBe(false);
    expect(events()).toHaveLength(1);
    expect(events()[0]?.detail.checked).toBe(false);
  });

  it('submits as a checkbox does', () => {
    const container = mount(`<form>${MARKUP}</form>`);
    const form = $<HTMLFormElement>(container, 'form');
    expect(new FormData(form).get('streaming')).toBe('on');

    $<HTMLInputElement>(container, 'input').click();
    expect(new FormData(form).get('streaming')).toBeNull();
  });
});

describe('<timlee-switch> — form-associated mode', () => {
  const MARKUP = '<timlee-switch name="streaming" value="yes" checked>Stream</timlee-switch>';

  it('becomes the field itself when there is no native control', () => {
    const el = $<TimleeSwitch>(mount(MARKUP), 'timlee-switch');
    expect(el.standalone).toBe(true);
    expect(el.getAttribute('role')).toBe('switch');
    expect(el.getAttribute('aria-checked')).toBe('true');
    expect(el.tabIndex).toBe(0);
    expect(el.querySelector('.timlee-switch__track')).toBeTruthy();
    expect(el.querySelector('.timlee-switch__track')?.getAttribute('aria-hidden')).toBe('true');
  });

  it('participates in the form through ElementInternals', () => {
    const container = mount(`<form>${MARKUP}</form>`);
    const form = $<HTMLFormElement>(container, 'form');
    const el = $<TimleeSwitch>(container, 'timlee-switch');

    expect(el.form).toBe(form);
    expect(new FormData(form).get('streaming')).toBe('yes');

    el.checked = false;
    // An unchecked switch submits nothing, exactly like a checkbox.
    expect(new FormData(form).get('streaming')).toBeNull();
  });

  it('resets to the value the server rendered', () => {
    const container = mount(`<form>${MARKUP}</form>`);
    const form = $<HTMLFormElement>(container, 'form');
    const el = $<TimleeSwitch>(container, 'timlee-switch');

    el.checked = false;
    form.reset();
    expect(el.checked).toBe(true);
    expect(new FormData(form).get('streaming')).toBe('yes');
  });

  it('toggles on Space and on Enter, and fires change', () => {
    const el = $<TimleeSwitch>(mount(MARKUP), 'timlee-switch');
    const changes = record(el, 'change');

    const space = press(el, ' ');
    expect(space.defaultPrevented).toBe(true);
    expect(el.checked).toBe(false);
    expect(el.getAttribute('aria-checked')).toBe('false');

    press(el, 'Enter');
    expect(el.checked).toBe(true);
    expect(changes()).toHaveLength(2);
  });

  it('toggles on click but not when a nested control was the target', () => {
    const container = mount(
      '<timlee-switch name="s">Label <a href="#docs" id="docs">docs</a></timlee-switch>',
    );
    const el = $<TimleeSwitch>(container, 'timlee-switch');

    click(el);
    expect(el.checked).toBe(true);

    click($(container, '#docs'));
    expect(el.checked).toBe(true);
  });

  it('ignores interaction while disabled and leaves the tab order', () => {
    const el = $<TimleeSwitch>(
      mount('<timlee-switch name="s" disabled>Off limits</timlee-switch>'),
      'timlee-switch',
    );
    expect(el.tabIndex).toBe(-1);
    expect(el.getAttribute('aria-disabled')).toBe('true');

    click(el);
    press(el, ' ');
    expect(el.checked).toBe(false);
  });

  it('restores state handed back by the browser', () => {
    const el = $<TimleeSwitch>(mount(MARKUP), 'timlee-switch');
    el.checked = false;
    el.formStateRestoreCallback('yes');
    expect(el.checked).toBe(true);
    el.formStateRestoreCallback(null);
    expect(el.checked).toBe(false);
  });
});
