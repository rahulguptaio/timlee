import { afterEach, describe, expect, it } from 'vitest';
import { userEvent } from 'vitest/browser';

import { $, cleanup, mount, press } from './test-utils';
import type { TimleeTooltip } from './tooltip';

afterEach(cleanup);

const MARKUP = `
  <timlee-tooltip>
    <button type="button" id="trigger">Rate limit</button>
    <span role="tooltip" id="tip">Requests per minute, per key.</span>
  </timlee-tooltip>
`;

function setup(markup = MARKUP) {
  const container = mount(markup);
  return {
    container,
    el: $<TimleeTooltip>(container, 'timlee-tooltip'),
    trigger: $<HTMLButtonElement>(container, '#trigger'),
    tip: $(container, '[role="tooltip"]'),
  };
}

describe('<timlee-tooltip>', () => {
  it('describes the trigger with the tooltip', () => {
    const { trigger, tip } = setup();
    expect(trigger.getAttribute('aria-describedby')).toBe(tip.id);
  });

  it('generates an id when the server did not', () => {
    const { trigger, tip } = setup(`
      <timlee-tooltip>
        <button type="button" id="trigger">?</button>
        <span role="tooltip">Help</span>
      </timlee-tooltip>
    `);
    expect(tip.id).toBeTruthy();
    expect(trigger.getAttribute('aria-describedby')).toBe(tip.id);
  });

  it('makes a non-interactive trigger reachable by keyboard', () => {
    const container = mount(`
      <timlee-tooltip>
        <abbr id="abbr">RPM</abbr>
        <span role="tooltip">Requests per minute</span>
      </timlee-tooltip>
    `);
    expect($(container, '#abbr').tabIndex).toBe(0);
  });

  it('promotes the tooltip into the top layer and shows it on focus', async () => {
    const { el, trigger, tip } = setup();
    expect(tip.getAttribute('popover')).toBe('manual');

    trigger.focus();
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(el.hasAttribute('open')).toBe(true);
    expect(tip.matches(':popover-open')).toBe(true);
  });

  it('can be seen once script has opened it', async () => {
    const { trigger, tip } = setup();
    trigger.focus();
    await new Promise((resolve) => setTimeout(resolve, 0));

    const style = getComputedStyle(tip);
    expect(style.visibility).toBe('visible');
    expect(style.opacity).toBe('1');
  });

  it('dismisses on Escape without moving focus (APG)', async () => {
    const { el, trigger, tip } = setup();
    trigger.focus();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(el.hasAttribute('open')).toBe(true);

    press(trigger, 'Escape');

    expect(el.hasAttribute('open')).toBe(false);
    expect(tip.matches(':popover-open')).toBe(false);
    expect(document.activeElement).toBe(trigger);
  });

  it('hides again when focus leaves', async () => {
    const { el, trigger } = setup();
    trigger.focus();
    await new Promise((resolve) => setTimeout(resolve, 0));

    trigger.blur();
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(el.hasAttribute('open')).toBe(false);
  });

  it('opens on hover after a delay rather than instantly', async () => {
    const { el } = setup();
    el.dispatchEvent(new PointerEvent('pointerenter', { bubbles: true }));
    expect(el.hasAttribute('open')).toBe(false);

    await new Promise((resolve) => setTimeout(resolve, 300));
    expect(el.hasAttribute('open')).toBe(true);
  });

  it('never swallows a pointer event aimed at the control it describes', () => {
    const { tip } = setup();
    expect(getComputedStyle(tip).pointerEvents).toBe('none');
  });

  describe('is hoverable (WCAG 1.4.13)', () => {
    // Room above, so the tooltip opens above the control as placed.
    const ROOMY = `<div style="padding-block-start:8rem">${MARKUP}</div>`;
    const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

    async function openByHover() {
      const parts = setup(ROOMY);
      await userEvent.hover(parts.trigger);
      await wait(300);
      expect(parts.el.hasAttribute('open')).toBe(true);
      return parts;
    }

    it('stays open while the pointer rests on it', async () => {
      const { el, tip } = await openByHover();

      // Forced: the move itself is under test, not Playwright's checks.
      await userEvent.hover(tip, { force: true });
      await wait(450);

      expect(el.hasAttribute('open')).toBe(true);
      expect(tip.matches(':popover-open')).toBe(true);
    });

    it('stays open while the pointer is in the gap on the way to it', async () => {
      const { el, tip } = await openByHover();
      expect(tip.getBoundingClientRect().bottom).toBeLessThan(
        $(el, '#trigger').getBoundingClientRect().top,
      );

      // A point between the tooltip and the control, which is no element's
      // box but the tooltip's bridge.
      const half = tip.getBoundingClientRect();
      await userEvent.hover(tip, {
        position: { x: half.width / 2, y: half.height + 4 },
        force: true,
      });
      await wait(450);

      expect(el.hasAttribute('open')).toBe(true);
    });

    it('still closes once the pointer leaves for somewhere else', async () => {
      const { el } = await openByHover();

      await userEvent.hover(document.body, { position: { x: 1, y: 1 }, force: true });
      await wait(450);

      expect(el.hasAttribute('open')).toBe(false);
    });
  });
});
