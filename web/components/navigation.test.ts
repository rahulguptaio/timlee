import { afterEach, describe, expect, it } from 'vitest';

import type { TimleePagination } from './navigation';
import { $, cleanup, mount } from './test-utils';

afterEach(cleanup);

describe('<timlee-breadcrumb>', () => {
  it('names the nav and marks the last crumb as the current page', () => {
    const container = mount(`
      <timlee-breadcrumb>
        <nav><ol>
          <li><a href="/">Dashboard</a></li>
          <li><a href="/keys">API keys</a></li>
          <li><a href="/keys/42">Production</a></li>
        </ol></nav>
      </timlee-breadcrumb>
    `);

    expect($(container, 'nav').getAttribute('aria-label')).toBe('Breadcrumb');
    const links = container.querySelectorAll('a');
    expect(links[2]?.getAttribute('aria-current')).toBe('page');
    expect(links[0]?.hasAttribute('aria-current')).toBe(false);
  });

  it('leaves an aria-current the server already set', () => {
    const container = mount(`
      <timlee-breadcrumb>
        <nav aria-label="You are here"><ol>
          <li><a href="/" aria-current="page">Dashboard</a></li>
          <li><a href="/keys">API keys</a></li>
        </ol></nav>
      </timlee-breadcrumb>
    `);
    expect($(container, 'nav').getAttribute('aria-label')).toBe('You are here');
    expect(container.querySelectorAll('[aria-current]')).toHaveLength(1);
    expect(container.querySelector('[aria-current]')?.textContent).toBe('Dashboard');
  });

  it('keeps the separator out of the accessibility tree', () => {
    const container = mount(`
      <timlee-breadcrumb>
        <nav><ol><li><a href="/">A</a></li><li><a href="/b">B</a></li></ol></nav>
      </timlee-breadcrumb>
    `);
    const items = container.querySelectorAll('li');
    const separator = getComputedStyle(items[1] as Element, '::before');
    expect(separator.content).toContain('/');
    // Generated content is not focusable, not selectable and not announced.
    expect(items[1]?.textContent).toBe('B');
  });
});

describe('<timlee-pagination>', () => {
  const MARKUP = `
    <timlee-pagination>
      <nav>
        <a href="?page=1" rel="prev">Previous</a>
        <a href="?page=1">1</a>
        <a href="?page=2" aria-current="page">2</a>
        <span data-ellipsis>…</span>
        <a href="?page=9">9</a>
        <a href="?page=3" rel="next">Next</a>
      </nav>
    </timlee-pagination>
  `;

  it('names the nav and every bare page number', () => {
    const container = mount(MARKUP);
    expect($(container, 'nav').getAttribute('aria-label')).toBe('Pagination');

    const links = Array.from(container.querySelectorAll('a'));
    expect(links.find((a) => a.textContent === '1')?.getAttribute('aria-label')).toBe('Page 1');
    expect(links.find((a) => a.textContent === '9')?.getAttribute('aria-label')).toBe('Page 9');
    // Words already say what they mean.
    expect(links.find((a) => a.textContent === 'Next')?.hasAttribute('aria-label')).toBe(false);
  });

  it('says which page is current', () => {
    const container = mount(MARKUP);
    expect($(container, '[aria-current="page"]').getAttribute('aria-label')).toBe(
      'Page 2, current page',
    );
  });

  it('leaves every page link independently tabbable', () => {
    const container = mount(MARKUP);
    for (const link of container.querySelectorAll('a')) {
      expect(link.hasAttribute('tabindex')).toBe(false);
    }
  });

  it('exposes the rel targets the server rendered', () => {
    const el = $<TimleePagination>(mount(MARKUP), 'timlee-pagination');
    expect(el.nextHref?.endsWith('?page=3')).toBe(true);
    expect(el.prevHref?.endsWith('?page=1')).toBe(true);
  });

  it('gives every control a 36px target', () => {
    const container = mount(MARKUP);
    for (const link of container.querySelectorAll('a')) {
      const box = link.getBoundingClientRect();
      expect(box.height).toBeGreaterThanOrEqual(36);
      expect(box.width).toBeGreaterThanOrEqual(36);
    }
  });
});

describe('<timlee-card>', () => {
  it('needs no JavaScript beyond registration', () => {
    const container = mount('<timlee-card><header><h3>Key</h3></header><p>Body</p></timlee-card>');
    const card = $(container, 'timlee-card');
    expect(card.children).toHaveLength(2);
    expect(getComputedStyle(card).borderTopWidth).toBe('1px');
  });

  it('stretches the marked link over the whole card without a click handler', () => {
    const container = mount(`
      <timlee-card interactive>
        <header><h3><a href="/keys/42" data-card-link>Production</a></h3></header>
        <p>Created 3 days ago.</p>
      </timlee-card>
    `);
    const overlay = getComputedStyle($(container, '[data-card-link]'), '::after');
    expect(overlay.content).not.toBe('none');
    expect(overlay.position).toBe('absolute');
  });

  it('keeps nested controls above the stretched overlay', () => {
    const container = mount(`
      <timlee-card interactive>
        <h3><a href="/a" data-card-link>A</a></h3>
        <timlee-button><button id="nested">Revoke</button></timlee-button>
      </timlee-card>
    `);
    expect(getComputedStyle($(container, 'timlee-button')).zIndex).toBe('1');
  });
});

describe('<timlee-empty-state>', () => {
  it('is layout only', () => {
    const container = mount(`
      <timlee-empty-state>
        <h2>No API keys</h2>
        <p>Create a key to start sending requests.</p>
      </timlee-empty-state>
    `);
    const el = $(container, 'timlee-empty-state');
    expect(el.children).toHaveLength(2);
    expect(getComputedStyle(el).display).toBe('flex');
    expect(getComputedStyle(el).borderStyle).toBe('dashed');
  });
});
