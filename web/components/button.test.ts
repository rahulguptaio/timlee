import { afterEach, describe, expect, it } from 'vitest';

import type { TimleeButton } from './button';
import { $, cleanup, mount } from './test-utils';

afterEach(cleanup);

describe('<timlee-button>', () => {
  it('leaves the server\u2019s control in place and wraps only its label', () => {
    const el = $<TimleeButton>(
      mount('<timlee-button variant="primary"><button type="submit">Save</button></timlee-button>'),
      'timlee-button',
    );
    const control = el.control as HTMLButtonElement;

    expect(control.tagName).toBe('BUTTON');
    expect(control.type).toBe('submit');
    expect(control.textContent).toBe('Save');
    expect(control.querySelector('.timlee-button__label')?.textContent).toBe('Save');
  });

  it('reflects loading between property and attribute, and marks the control busy', () => {
    const el = $<TimleeButton>(
      mount('<timlee-button><button>Send</button></timlee-button>'),
      'timlee-button',
    );
    const control = el.control as HTMLButtonElement;

    expect(control.getAttribute('aria-busy')).toBe('false');
    expect(control.disabled).toBe(false);

    el.loading = true;
    expect(el.hasAttribute('loading')).toBe(true);
    expect(control.getAttribute('aria-busy')).toBe('true');
    // A busy submit button must actually stop submitting, not merely say so.
    expect(control.disabled).toBe(true);
    expect(control.querySelector('.timlee-button__spinner')).toBeTruthy();
    expect(control.querySelector('.timlee-button__spinner')?.getAttribute('aria-hidden')).toBe(
      'true',
    );

    el.loading = false;
    expect(el.hasAttribute('loading')).toBe(false);
    expect(control.disabled).toBe(false);
    expect(control.querySelector('.timlee-button__spinner')).toBeNull();
  });

  it('does not submit the form while loading', () => {
    const container = mount(
      '<form><timlee-button loading><button type="submit">Save</button></timlee-button></form>',
    );
    const form = $<HTMLFormElement>(container, 'form');
    let submits = 0;
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      submits++;
    });

    $<HTMLButtonElement>(container, 'button').click();
    expect(submits).toBe(0);
  });

  it('uses aria-disabled and removes the tab stop for a link', () => {
    const el = $<TimleeButton>(
      mount('<timlee-button disabled><a href="/x">Docs</a></timlee-button>'),
      'timlee-button',
    );
    const link = el.control as HTMLAnchorElement;

    expect(link.getAttribute('aria-disabled')).toBe('true');
    expect(link.getAttribute('tabindex')).toBe('-1');

    el.disabled = false;
    expect(link.getAttribute('aria-disabled')).toBe('false');
    expect(link.hasAttribute('tabindex')).toBe(false);
  });

  it('survives having no control rather than inventing one', () => {
    const el = $<TimleeButton>(mount('<timlee-button>orphan</timlee-button>'), 'timlee-button');
    expect(el.control).toBeNull();
    expect(el.querySelector('button')).toBeNull();
  });

  it('meets the 24x24 minimum target size at every size', () => {
    const container = mount(`
      <timlee-button size="sm"><button>S</button></timlee-button>
      <timlee-button><button>M</button></timlee-button>
      <timlee-button size="lg"><button>L</button></timlee-button>
    `);
    for (const control of container.querySelectorAll('button')) {
      const box = control.getBoundingClientRect();
      expect(box.height, control.textContent ?? '').toBeGreaterThanOrEqual(24);
      expect(box.width, control.textContent ?? '').toBeGreaterThanOrEqual(24);
    }
  });
});

describe('<timlee-icon-button>', () => {
  it('promotes title to an accessible name when there is none', () => {
    const container = mount(
      '<timlee-icon-button><button title="Copy key"><svg aria-hidden="true"></svg></button></timlee-icon-button>',
    );
    expect($(container, 'button').getAttribute('aria-label')).toBe('Copy key');
  });

  it('prefers the host label attribute over the title', () => {
    const container = mount(
      '<timlee-icon-button label="Revoke key"><button title="Revoke"><svg aria-hidden="true"></svg></button></timlee-icon-button>',
    );
    expect($(container, 'button').getAttribute('aria-label')).toBe('Revoke key');
  });

  it('leaves an existing accessible name alone', () => {
    const container = mount(
      '<timlee-icon-button label="x"><button aria-label="Close"><svg></svg></button></timlee-icon-button>',
    );
    expect($(container, 'button').getAttribute('aria-label')).toBe('Close');
  });

  it('is at least 36px square', () => {
    const container = mount(
      '<timlee-icon-button size="sm"><button aria-label="Copy"><svg></svg></button></timlee-icon-button>',
    );
    const box = $(container, 'button').getBoundingClientRect();
    expect(box.width).toBeGreaterThanOrEqual(36);
    expect(box.height).toBeGreaterThanOrEqual(36);
  });
});
