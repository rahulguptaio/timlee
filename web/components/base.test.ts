import { afterEach, describe, expect, it } from 'vitest';
import { RovingTabindex } from './a11y';
import { announce, resetLiveRegions } from './announce';
import { ensureId, focusableWithin, READY_ATTR, reflectBool, uid, whenParsed } from './base';
import { TIMLEE_TAGS } from './index';
import { $, cleanup, mount, press, settle } from './test-utils';

// Read before any test's cleanup can remove them.
const LIVE_AT_LOAD = Array.from(document.querySelectorAll('[data-timlee-live]'), (region) =>
  region.getAttribute('data-timlee-live'),
);

afterEach(cleanup);

describe('registration', () => {
  it('defines every advertised tag', async () => {
    for (const tag of TIMLEE_TAGS) {
      expect(customElements.get(tag), tag).toBeTypeOf('function');
    }
  });

  it('upgrades elements parsed from markup, not just constructed ones', () => {
    const container = mount('<timlee-badge tone="success">Live</timlee-badge>');
    const badge = $(container, 'timlee-badge');
    expect(badge.constructor.name).toBe('TimleeBadge');
    expect(badge.hasAttribute(READY_ATTR)).toBe(true);
    expect(badge.textContent).toBe('Live');
  });

  it('hydrates once even when the element is moved between parents', () => {
    const container = mount('<timlee-badge removable>gpt-4o</timlee-badge>');
    const badge = $(container, 'timlee-badge');
    const other = mount('<section></section>').firstElementChild as HTMLElement;

    other.append(badge);
    expect(badge.querySelectorAll('.timlee-badge__remove')).toHaveLength(1);
  });
});

describe('attribute helpers', () => {
  it('does not touch the attribute when the value is unchanged', () => {
    const el = document.createElement('div');
    let changes = 0;
    new MutationObserver(() => changes++).observe(el, { attributes: true });

    reflectBool(el, 'open', true);
    reflectBool(el, 'open', true);
    expect(el.hasAttribute('open')).toBe(true);

    reflectBool(el, 'open', false);
    expect(el.hasAttribute('open')).toBe(false);
    expect(changes).toBeLessThanOrEqual(2);
  });

  it('generates ids that do not already exist in the document', () => {
    const container = mount('<p id="timlee-hint-1">taken</p>');
    const node = document.createElement('p');
    container.append(node);
    const id = ensureId(node, 'timlee-hint');
    expect(id).not.toBe('timlee-hint-1');
    expect(ensureId(node, 'timlee-hint')).toBe(id);
    expect(uid('x')).not.toBe(uid('x'));
  });
});

describe('focusableWithin', () => {
  it('skips disabled, hidden and inert candidates', () => {
    const container = mount(`
      <div>
        <button id="a">a</button>
        <button id="b" disabled>b</button>
        <div inert><button id="c">c</button></div>
        <button id="d" style="display:none">d</button>
        <a id="e" href="#x">e</a>
        <span id="f" tabindex="-1">f</span>
      </div>
    `);
    const ids = focusableWithin(container).map((el) => el.id);
    expect(ids).toEqual(['a', 'e']);
  });
});

describe('whenParsed', () => {
  it('runs immediately once the document is past loading', async () => {
    let ran = false;
    whenParsed(() => {
      ran = true;
    });
    expect(ran).toBe(true);
  });
});

describe('announce', () => {
  it('has both live regions in the page before the first message', () => {
    // A region that appears with its first message is not reliably announced.
    expect(LIVE_AT_LOAD).toEqual(['polite', 'assertive']);
  });

  it('speaks through the layout\u2019s own announcers rather than adding more', async () => {
    resetLiveRegions();
    const container = mount(`
      <p role="status" aria-live="polite" aria-atomic="true" data-announcer></p>
      <p role="alert" aria-live="assertive" aria-atomic="true" data-announcer="assertive"></p>
    `);

    announce('Key revoked.');
    announce('Copy failed.', 'assertive');
    await settle();

    expect($(container, '[data-announcer=""]').textContent).toBe('Key revoked.');
    expect($(container, '[data-announcer="assertive"]').textContent).toBe('Copy failed.');
    expect(document.querySelectorAll('[data-timlee-live]')).toHaveLength(0);
  });

  describe('while a modal dialog is open', () => {
    const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
    const MARKUP = `
      <p role="status" aria-live="polite" aria-atomic="true" data-announcer></p>
      <dialog aria-label="Preferences"><button type="button">Done</button></dialog>
    `;
    const politeRegions = () =>
      document.querySelectorAll(
        '[data-announcer]:not([data-announcer="assertive"]), [data-timlee-live="polite"]',
      );

    it('speaks from inside the dialog, since the page behind it is inert', async () => {
      resetLiveRegions();
      const container = mount(MARKUP);
      const region = $(container, '[data-announcer]');
      const dialog = $<HTMLDialogElement>(container, 'dialog');
      dialog.showModal();

      announce('Copied to clipboard');
      await wait(250);

      // A popover over the dialog would be inert as well; inside it is not.
      expect(dialog.contains(region)).toBe(true);
      expect(region.textContent).toBe('Copied to clipboard');
      expect(politeRegions()).toHaveLength(1);

      const closed = new Promise((resolve) => dialog.addEventListener('close', resolve));
      dialog.close();
      await closed;
      expect(region.parentElement).toBe(container);
      expect(politeRegions()).toHaveLength(1);
    });

    it('takes its announcer back from a dialog removed while open', async () => {
      resetLiveRegions();
      const container = mount(MARKUP);
      const region = $(container, '[data-announcer]');
      const dialog = $<HTMLDialogElement>(container, 'dialog');
      dialog.showModal();
      announce('Saving.');
      await wait(250);

      // Removed open, as a page swapping its content would: no close event.
      dialog.remove();
      announce('Saved.');
      await wait(250);

      expect(region.parentElement).toBe(container);
      expect(region.textContent).toBe('Saved.');
      expect(politeRegions()).toHaveLength(1);
    });
  });
});

describe('RovingTabindex', () => {
  function setup(orientation: 'horizontal' | 'vertical' = 'horizontal') {
    const container = mount(
      '<div><button>1</button><button disabled>2</button><button>3</button></div>',
    );
    const items = Array.from(container.querySelectorAll('button'));
    const activated: HTMLElement[] = [];
    const roving = new RovingTabindex({ orientation, onActivate: (item) => activated.push(item) });
    roving.setItems(items, items[0] ?? null);
    return { items, roving, activated };
  }

  it('keeps exactly one item in the tab order', () => {
    const { items } = setup();
    expect(items.map((i) => i.tabIndex)).toEqual([0, -1, -1]);
  });

  it('skips disabled items and wraps at the ends', () => {
    const { items, roving, activated } = setup();
    const first = items[0];
    const third = items[2];
    if (!first || !third) throw new Error('fixture');

    roving.handleKeydown(press(first, 'ArrowRight'));
    expect(activated.at(-1)).toBe(third);
    expect(third.tabIndex).toBe(0);

    roving.handleKeydown(press(third, 'ArrowRight'));
    expect(activated.at(-1)).toBe(first);
  });

  it('uses the vertical arrows when the orientation says so', () => {
    const { items, roving, activated } = setup('vertical');
    const first = items[0];
    if (!first) throw new Error('fixture');

    expect(roving.handleKeydown(press(first, 'ArrowRight'))).toBe(false);
    expect(roving.handleKeydown(press(first, 'ArrowDown'))).toBe(true);
    expect(activated.at(-1)).toBe(items[2]);
  });

  it('answers Home and End', () => {
    const { items, roving, activated } = setup();
    const first = items[0];
    if (!first) throw new Error('fixture');
    roving.handleKeydown(press(first, 'End'));
    expect(activated.at(-1)).toBe(items[2]);
    const third = items[2];
    if (!third) throw new Error('fixture');
    roving.handleKeydown(press(third, 'Home'));
    expect(activated.at(-1)).toBe(items[0]);
  });
});
