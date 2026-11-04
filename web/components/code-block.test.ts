import { afterEach, describe, expect, it, vi } from 'vitest';

import type { TimleeCodeBlock } from './code-block';
import { $, cleanup, countScrollWidthReads, mount, moveAround, record, settle } from './test-utils';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const CODE = 'curl https://api.timlee.dev/v1/chat/completions';
const MARKUP = `
  <timlee-code-block label="curl example" language="bash">
    <pre><code>${CODE}</code></pre>
  </timlee-code-block>
`;

function stubClipboard(impl: (text: string) => Promise<void>) {
  return vi.spyOn(navigator.clipboard, 'writeText').mockImplementation(impl);
}

describe('<timlee-code-block>', () => {
  it('leaves the code readable and selectable', () => {
    const el = $<TimleeCodeBlock>(mount(MARKUP), 'timlee-code-block');
    expect(el.code).toBe(CODE);
    expect(el.querySelector('code')?.textContent).toBe(CODE);
  });

  it('adds a copy button with a specific accessible name', () => {
    const container = mount(MARKUP);
    const button = $<HTMLButtonElement>(container, '.timlee-code-block__copy');
    expect(button.getAttribute('aria-label')).toBe('Copy \u2068curl example\u2069');
    expect(button.type).toBe('button');
    // SC 2.5.3: the visible word has to be part of the accessible name.
    expect(button.getAttribute('aria-label')).toContain(button.textContent);
  });

  it('copies and announces through the shared live region', async () => {
    const writes: string[] = [];
    stubClipboard(async (text) => {
      writes.push(text);
    });

    const container = mount(MARKUP);
    const el = $<TimleeCodeBlock>(container, 'timlee-code-block');
    const events = record<CustomEvent<{ ok: boolean }>>(el, 'timlee-copy');

    expect(await el.copy()).toBe(true);
    await settle();

    expect(writes).toEqual([CODE]);
    expect(events()[0]?.detail.ok).toBe(true);
    const live = document.querySelector('[data-timlee-live="polite"]');
    expect(live?.textContent).toBe('Copied to clipboard');
    // Visible text and accessible name move together, or SC 2.5.3 breaks.
    const button = $(container, '.timlee-code-block__copy');
    expect(button.textContent).toBe('Copied');
    expect(button.getAttribute('aria-label')).toBe('Copied \u2068curl example\u2069');
  });

  it('copies exactly once after the block is moved and put back', async () => {
    const writes: string[] = [];
    stubClipboard(async (text) => {
      writes.push(text);
    });
    const container = mount(MARKUP);
    // settings.ts moves a still-visible API key like this on every swap, and
    // the key is shown only once: its Copy button must keep working.
    moveAround($(container, 'timlee-code-block'));

    $<HTMLButtonElement>(container, '.timlee-code-block__copy').click();
    await settle();

    expect(writes).toEqual([CODE]);
  });

  it('reports a clipboard failure assertively instead of silently', async () => {
    stubClipboard(async () => {
      throw new Error('denied');
    });

    const el = $<TimleeCodeBlock>(mount(MARKUP), 'timlee-code-block');
    const events = record<CustomEvent<{ ok: boolean }>>(el, 'timlee-copy');

    expect(await el.copy()).toBe(false);
    await settle();

    expect(events()[0]?.detail.ok).toBe(false);
    expect(document.querySelector('[data-timlee-live="assertive"]')?.textContent).toContain(
      'Copy failed',
    );
  });

  it('omits the copy button when asked', () => {
    const container = mount(MARKUP.replace('label=', 'no-copy label='));
    expect(container.querySelector('.timlee-code-block__copy')).toBeNull();
  });

  it('measures its overflow after layout, never while it upgrades', async () => {
    const reads = countScrollWidthReads();
    try {
      const el = $<TimleeCodeBlock>(mount(MARKUP), 'timlee-code-block');
      // A read here would force the page's layout in the upgrade's task.
      expect(reads.count).toBe(0);
      el.style.inlineSize = '120px';
      await settle();
      expect(reads.count).toBeGreaterThan(0);
      expect(el.querySelector('pre')?.getAttribute('role')).toBe('region');
    } finally {
      reads.restore();
    }
  });

  it('makes an overflowing pre keyboard-scrollable, and only then', async () => {
    const el = $<TimleeCodeBlock>(mount(MARKUP), 'timlee-code-block');
    const pre = el.querySelector('pre') as HTMLPreElement;

    el.style.inlineSize = '900px';
    await settle();
    expect(pre.hasAttribute('tabindex')).toBe(false);

    el.style.inlineSize = '120px';
    await settle();
    expect(pre.getAttribute('tabindex')).toBe('0');
    expect(pre.getAttribute('role')).toBe('region');
    expect(pre.getAttribute('aria-label')).toBe('\u2068curl example\u2069, scrollable');

    // Its name goes with the role: aria-label is prohibited on a plain <pre>.
    el.style.inlineSize = '900px';
    await settle();
    expect(pre.hasAttribute('tabindex')).toBe(false);
    expect(pre.hasAttribute('role')).toBe(false);
    expect(pre.hasAttribute('aria-label')).toBe(false);
  });

  describe('in off-screen content-visibility: auto content', () => {
    // Far below the fold, so the browser skips the section: nothing in it is
    // laid out, and no ResizeObserver reports, until the reader gets near.
    const skipped = (code: string, width = '') => `
      <div style="block-size:300vh"></div>
      <section style="content-visibility:auto;contain-intrinsic-size:auto 20rem">
        <timlee-code-block label="curl example" style="${width}"><pre><code>${code}</code></pre></timlee-code-block>
      </section>
    `;

    it('is a keyboard-reachable region before it can be measured', async () => {
      const container = mount(skipped(CODE, 'inline-size:120px'));
      const pre = $(container, 'pre');
      await settle();
      await settle();

      // An audit that forces its layout finds it scrolling, so it has to be
      // reachable already (axe scrollable-region-focusable: 5 at 320 px on /).
      expect(pre.getAttribute('tabindex')).toBe('0');
      expect(pre.getAttribute('role')).toBe('region');
      expect(pre.getAttribute('aria-label')).toBe('\u2068curl example\u2069, scrollable');
    });

    it('is measured once it renders, and gives a needless tab stop back', async () => {
      const container = mount(skipped('ls'));
      const pre = $(container, 'pre');
      await settle();
      await settle();
      expect(pre.getAttribute('tabindex')).toBe('0');

      $(container, 'section').scrollIntoView();
      await settle();

      expect(pre.hasAttribute('tabindex')).toBe(false);
      expect(pre.hasAttribute('role')).toBe(false);
      expect(pre.hasAttribute('aria-label')).toBe(false);
    });

    it('keeps an offered tab stop while it has focus', async () => {
      const container = mount(skipped('ls'));
      const pre = $(container, 'pre');
      await settle();
      await settle();

      // Focus renders the section, which shows the block fits, while the
      // keyboard is on it: taking the tab stop now would drop focus.
      pre.focus();
      await settle();
      expect(document.activeElement).toBe(pre);
      expect(pre.getAttribute('tabindex')).toBe('0');

      pre.blur();
      await settle();
      expect(pre.hasAttribute('tabindex')).toBe(false);
    });
  });

  it('numbers scrolling blocks that share a name, so each region is distinct', async () => {
    const block = (label: string) =>
      `<timlee-code-block label="${label}" style="inline-size:120px"><pre><code>${CODE}</code></pre></timlee-code-block>`;
    const container = mount(block('json example') + block('bash example') + block('json example'));
    await settle();

    const names = Array.from(container.querySelectorAll('pre'), (pre) =>
      pre.getAttribute('aria-label'),
    );
    // axe landmark-unique: two regions both called "json example" cannot be
    // told apart in a landmark list.
    expect(names).toEqual([
      '\u2068json example 1\u2069, scrollable',
      '\u2068bash example\u2069, scrollable',
      '\u2068json example 2\u2069, scrollable',
    ]);
  });
});
