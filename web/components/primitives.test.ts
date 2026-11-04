import { afterEach, describe, expect, it } from 'vitest';

import type { TimleeAlert } from './alert';
import type { TimleeAvatar } from './avatar';
import { initialsOf } from './avatar';
import type { TimleeBadge } from './badge';
import type { TimleeProgress, TimleeSpinner } from './spinner';
import { $, cleanup, mount, moveAround, record } from './test-utils';

afterEach(cleanup);

describe('<timlee-badge>', () => {
  it('is plain markup until it is asked to be removable', () => {
    const container = mount('<timlee-badge tone="success" dot>Active</timlee-badge>');
    expect(container.querySelector('.timlee-badge__remove')).toBeNull();
    expect($(container, 'timlee-badge').textContent).toBe('Active');
  });

  it('names its remove control after the badge it removes', () => {
    const container = mount('<timlee-badge removable>gpt-4o-mini</timlee-badge>');
    expect($(container, '.timlee-badge__remove').getAttribute('aria-label')).toBe(
      'Remove \u2068gpt-4o-mini\u2069',
    );
  });

  it('uses pseudo-RTL copy without letting an identifier reorder it', () => {
    const block = JSON.stringify({
      lang: 'ar-XB',
      intl: 'ar-u-nu-latn',
      dir: 'rtl',
      m: {
        'components.badge.remove': ['\u202eRemove \u202c\u2066', ['label'], '\u2069'],
      },
    });
    const container = mount(
      `<script type="application/json" id="timlee-i18n">${block}</script>` +
        '<timlee-badge removable>gpt-4o-mini</timlee-badge>',
    );

    expect($(container, '.timlee-badge__remove').getAttribute('aria-label')).toBe(
      '\u202eRemove \u202c\u2066\u2068gpt-4o-mini\u2069\u2069',
    );
  });

  it('emits a cancelable removal carrying the value', () => {
    const container = mount('<timlee-badge removable value="m1">gpt-4o-mini</timlee-badge>');
    const el = $<TimleeBadge>(container, 'timlee-badge');
    const events = record<CustomEvent<{ value: string }>>(el, 'timlee-remove');

    $<HTMLButtonElement>(container, '.timlee-badge__remove').click();

    expect(events()[0]?.detail.value).toBe('m1');
    expect(el.isConnected).toBe(false);
  });

  it('still removes itself exactly once after it is moved and put back', () => {
    const container = mount('<timlee-badge removable value="m1">gpt-4o-mini</timlee-badge>');
    const el = $<TimleeBadge>(container, 'timlee-badge');
    const events = record(el, 'timlee-remove');
    moveAround(el);

    $<HTMLButtonElement>(container, '.timlee-badge__remove').click();

    expect(events()).toHaveLength(1);
    expect(el.isConnected).toBe(false);
  });

  it('stays when a listener takes responsibility for it', () => {
    const container = mount('<timlee-badge removable>keep</timlee-badge>');
    const el = $<TimleeBadge>(container, 'timlee-badge');
    el.addEventListener('timlee-remove', (event) => event.preventDefault());

    $<HTMLButtonElement>(container, '.timlee-badge__remove').click();
    expect(el.isConnected).toBe(true);
  });

  it('gives the remove control a 24px hit area (SC 2.5.8)', () => {
    const container = mount('<timlee-badge removable>x</timlee-badge>');
    const button = $(container, '.timlee-badge__remove');
    const hit = getComputedStyle(button, '::before');
    expect(hit.content).not.toBe('none');
    const box = button.getBoundingClientRect();
    // 16px glyph plus 5px of ::before bleed on each side.
    expect(box.width + 10).toBeGreaterThanOrEqual(24);
    expect(box.height + 10).toBeGreaterThanOrEqual(24);
  });
});

describe('<timlee-alert>', () => {
  it('does not claim a live region for server-rendered content', () => {
    const el = $<TimleeAlert>(
      mount('<timlee-alert tone="warning"><strong>Quota</strong></timlee-alert>'),
      'timlee-alert',
    );
    // A live region announces *changes*; this one was there on first paint.
    expect(el.hasAttribute('role')).toBe(document.readyState === 'complete');
  });

  it('becomes a live region when script inserts it after load', () => {
    const container = mount('<div></div>');
    const alert = document.createElement('timlee-alert');
    alert.setAttribute('tone', 'danger');
    alert.textContent = 'Upstream timeout.';
    container.firstElementChild?.append(alert);

    expect(alert.getAttribute('role')).toBe('alert');
  });

  it('adds a dismiss control only when dismissible', () => {
    const plain = mount('<timlee-alert>Note</timlee-alert>');
    expect(plain.querySelector('.timlee-alert__dismiss')).toBeNull();

    const container = mount('<timlee-alert dismissible>Note</timlee-alert>');
    const el = $<TimleeAlert>(container, 'timlee-alert');
    const button = $<HTMLButtonElement>(container, '.timlee-alert__dismiss');
    expect(button.getAttribute('aria-label')).toBe('Dismiss');

    button.click();
    expect(el.isConnected).toBe(false);
  });

  it('still dismisses exactly once after it is moved and put back', () => {
    const container = mount('<timlee-alert dismissible>Note</timlee-alert>');
    const el = $<TimleeAlert>(container, 'timlee-alert');
    const events = record(el, 'timlee-dismiss');
    moveAround(el);

    $<HTMLButtonElement>(container, '.timlee-alert__dismiss').click();

    expect(events()).toHaveLength(1);
    expect(el.isConnected).toBe(false);
  });

  it('distinguishes tones by shape as well as colour', () => {
    const container = mount(`
      <timlee-alert tone="success">ok</timlee-alert>
      <timlee-alert tone="warning">careful</timlee-alert>
      <timlee-alert tone="danger">bad</timlee-alert>
    `);
    const shapes = Array.from(container.querySelectorAll('timlee-alert')).map(
      (el) => getComputedStyle(el, '::before').clipPath,
    );
    expect(new Set(shapes).size).toBe(3);
  });
});

describe('<timlee-avatar>', () => {
  it('shows the server\u2019s image and nothing else', () => {
    const container = mount(
      '<timlee-avatar><img src="data:image/gif;base64,R0lGODlhAQABAAAAACH5BAEKAAEALAAAAAABAAEAAAICTAEAOw==" alt="Rahul Kumar"></timlee-avatar>',
    );
    expect(container.querySelector('span')).toBeNull();
    expect($<HTMLImageElement>(container, 'img').alt).toBe('Rahul Kumar');
  });

  it('falls back to initials when the image fails, keeping the img for a retry', async () => {
    const container = mount(
      '<timlee-avatar><img src="/missing.png" alt="Rahul Kumar"></timlee-avatar>',
    );
    const el = $<TimleeAvatar>(container, 'timlee-avatar');
    const img = $<HTMLImageElement>(container, 'img');

    img.dispatchEvent(new Event('error'));

    expect(img.hidden).toBe(true);
    expect(img.isConnected).toBe(true);
    expect(el.querySelector('span')?.textContent).toBe('RK');
    // Initials are decoration; the name is the img's alt.
    expect(el.querySelector('span')?.getAttribute('aria-hidden')).toBe('true');
  });

  it('still falls back exactly once when the image fails after a move', () => {
    const container = mount(
      '<timlee-avatar><img src="/missing.png" alt="Rahul Kumar"></timlee-avatar>',
    );
    const el = $<TimleeAvatar>(container, 'timlee-avatar');
    const img = $<HTMLImageElement>(container, 'img');
    moveAround(el);
    const fallbacks = new MutationObserver(() => {});
    fallbacks.observe(img, { attributeFilter: ['hidden'] });

    img.dispatchEvent(new Event('error'));

    expect(fallbacks.takeRecords()).toHaveLength(1);
    expect(el.querySelector('span')?.textContent).toBe('RK');
  });

  it('is a named image when it has no img at all', () => {
    const el = $<TimleeAvatar>(
      mount('<timlee-avatar name="Timlee Ops"></timlee-avatar>'),
      'timlee-avatar',
    );
    expect(el.getAttribute('role')).toBe('img');
    expect(el.getAttribute('aria-label')).toBe('Timlee Ops');
    expect(el.textContent).toBe('TO');
  });

  it('puts the status into the accessible name, not only the colour', () => {
    const el = $<TimleeAvatar>(
      mount('<timlee-avatar name="Rahul Kumar" status="online"></timlee-avatar>'),
      'timlee-avatar',
    );
    expect(el.getAttribute('aria-label')).toBe('Rahul Kumar, online');
  });

  it('derives initials from the first and last words', () => {
    expect(initialsOf('Rahul Kumar')).toBe('RK');
    expect(initialsOf('madonna')).toBe('M');
    expect(initialsOf('Ada  Byron   Lovelace')).toBe('AL');
    expect(initialsOf('   ')).toBe('');
  });

  it('leaves text content the server wrote alone', () => {
    const el = $<TimleeAvatar>(mount('<timlee-avatar>42</timlee-avatar>'), 'timlee-avatar');
    expect(el.textContent).toBe('42');
  });
});

describe('<timlee-spinner>', () => {
  it('is a named indeterminate progressbar', () => {
    const el = $<TimleeSpinner>(
      mount('<timlee-spinner label="Loading models"></timlee-spinner>'),
      'timlee-spinner',
    );
    expect(el.getAttribute('role')).toBe('progressbar');
    expect(el.getAttribute('aria-label')).toBe('Loading models');
    // Indeterminate means no value, which is how "unknown duration" is said.
    expect(el.hasAttribute('aria-valuenow')).toBe(false);
  });

  it('falls back to a generic name rather than none', () => {
    const el = $<TimleeSpinner>(mount('<timlee-spinner></timlee-spinner>'), 'timlee-spinner');
    expect(el.getAttribute('aria-label')).toBe('Loading');
  });

  it('needs no children to render', () => {
    const el = $<TimleeSpinner>(mount('<timlee-spinner></timlee-spinner>'), 'timlee-spinner');
    expect(el.childNodes).toHaveLength(0);
    expect(el.getBoundingClientRect().width).toBeGreaterThan(0);
  });

  it('tracks a changed label', () => {
    const el = $<TimleeSpinner>(mount('<timlee-spinner></timlee-spinner>'), 'timlee-spinner');
    el.setAttribute('label', 'Rotating key');
    expect(el.getAttribute('aria-label')).toBe('Rotating key');
  });
});

describe('<timlee-progress>', () => {
  const MARKUP = `
    <timlee-progress show-value>
      <label>Uploading</label>
      <progress value="40" max="100"></progress>
    </timlee-progress>
  `;

  it('uses the native element the server rendered', () => {
    const el = $<TimleeProgress>(mount(MARKUP), 'timlee-progress');
    expect(el.control?.tagName).toBe('PROGRESS');
    expect(el.value).toBe(40);
    expect(($(el, 'label') as HTMLLabelElement).control).toBe(el.control);
  });

  it('renders the percentage when asked', () => {
    const container = mount(MARKUP);
    expect($(container, '.timlee-progress__value').textContent).toBe('40%');
  });

  it('distinguishes indeterminate from zero', () => {
    const el = $<TimleeProgress>(
      mount('<timlee-progress label="Working"><progress></progress></timlee-progress>'),
      'timlee-progress',
    );
    expect(el.value).toBeNull();
    el.value = 0;
    expect(el.value).toBe(0);
  });

  it('labels a progress bar that has no label element', () => {
    const container = mount(
      '<timlee-progress label="Rotating"><progress value="1" max="4"></progress></timlee-progress>',
    );
    expect($(container, 'progress').getAttribute('aria-label')).toBe('Rotating');
  });
});

describe('<timlee-skeleton>', () => {
  it('is hidden from assistive technology', () => {
    const container = mount('<timlee-skeleton lines="3"></timlee-skeleton>');
    expect($(container, 'timlee-skeleton').getAttribute('aria-hidden')).toBe('true');
  });

  it('draws its lines without child elements', () => {
    const container = mount('<timlee-skeleton lines="3"></timlee-skeleton>');
    const el = $(container, 'timlee-skeleton');
    expect(el.childNodes).toHaveLength(0);
    expect(el.getBoundingClientRect().height).toBeGreaterThan(30);
  });
});
