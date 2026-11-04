/**
 * server-render.test.ts — the single most important test in this library.
 *
 * Timlee's HTML comes out of Go `html/template`. These components are only
 * worth anything if that HTML is complete and usable *before* the bundle
 * loads, and if upgrading it in place loses nothing.
 *
 * So this test does not construct a single element in JavaScript. It builds
 * the exact bytes a Go handler would write — markup plus the two stylesheets
 * the asset pipeline serves — loads them into a document with no script at
 * all, proves the page works, and only then loads the module and proves the
 * upgrade.
 */

import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import baseCss from '../assets/css/base.css?raw';
import tokensCss from '../assets/css/tokens.css?raw';
import componentsCss from './components.css?inline';
import { englishComponentBlock } from './i18n-fixture';

/** The module URL Vite serves, which is what the iframe will import. */
const LIBRARY_URL = new URL('./index.ts', import.meta.url).href;

/**
 * Exactly what a Go template emits: no wrapper divs added by script and no
 * `<template>`. Every component is in its server form, with the CSP-safe
 * message data its eventual upgrade can use.
 */
const SERVER_HTML = `
<script type="application/json" id="timlee-i18n">${JSON.stringify(englishComponentBlock())}</script>
<main>
  <h1 id="page-title">API keys</h1>

  <form id="key-form" action="/keys" method="post">
    <timlee-input>
      <label for="key-name">Key name</label>
      <input id="key-name" name="name" type="text" value="Production" required>
      <p data-hint>Shown in the dashboard and in audit logs.</p>
      <p data-error></p>
    </timlee-input>

    <timlee-select>
      <label for="key-env">Environment</label>
      <select id="key-env" name="env">
        <option value="prod" selected>Production</option>
        <option value="staging">Staging</option>
      </select>
    </timlee-select>

    <timlee-checkbox>
      <label>
        <input type="checkbox" name="rotate" value="1" checked>
        <span>Rotate automatically</span>
      </label>
    </timlee-checkbox>

    <timlee-switch>
      <label>
        <input type="checkbox" role="switch" name="streaming" checked>
        <span>Stream responses</span>
      </label>
    </timlee-switch>

    <timlee-button variant="primary">
      <button type="submit">Create key</button>
    </timlee-button>
  </form>

  <timlee-tabs>
    <nav data-tablist aria-label="Key details">
      <a href="#panel-usage" aria-current="page">Usage</a>
      <a href="#panel-limits">Limits</a>
    </nav>
    <section id="panel-usage" data-tabpanel><p>4,812 requests today.</p></section>
    <section id="panel-limits" data-tabpanel><p>60 requests per minute.</p></section>
  </timlee-tabs>

  <timlee-tooltip>
    <button type="button" id="rpm-trigger">Rate limit</button>
    <span role="tooltip" id="rpm-tip">Requests per minute, per key.</span>
  </timlee-tooltip>

  <timlee-toaster>
    <timlee-toast tone="success">API key created.</timlee-toast>
  </timlee-toaster>

  <timlee-code-block label="curl example">
    <pre><code>curl https://api.timlee.dev/v1/chat/completions</code></pre>
  </timlee-code-block>

  <timlee-table label="Recent keys" sortable>
    <table>
      <thead><tr><th scope="col" data-sort="text">Name</th><th scope="col">Created</th></tr></thead>
      <tbody>
        <tr><td>Staging</td><td>2 days ago</td></tr>
        <tr><td>Production</td><td>8 days ago</td></tr>
      </tbody>
    </table>
  </timlee-table>

  <timlee-badge tone="success" dot>Active</timlee-badge>
  <timlee-alert tone="warning"><strong>Approaching your quota.</strong></timlee-alert>
  <timlee-spinner label="Loading models"></timlee-spinner>
  <timlee-skeleton lines="3"></timlee-skeleton>

  <timlee-breadcrumb>
    <nav><ol><li><a href="/">Dashboard</a></li><li><a href="/keys">API keys</a></li></ol></nav>
  </timlee-breadcrumb>

  <timlee-pagination>
    <nav><a href="?page=1" aria-current="page">1</a><a href="?page=2">2</a></nav>
  </timlee-pagination>

  <timlee-dialog id="revoke" dismissible>
    <dialog aria-labelledby="revoke-title">
      <form method="dialog">
        <h2 id="revoke-title">Revoke this key?</h2>
        <menu><button value="cancel">Cancel</button><button value="revoke">Revoke</button></menu>
      </form>
    </dialog>
  </timlee-dialog>
  <a href="/keys/42/revoke" data-dialog-open="revoke" id="revoke-trigger">Revoke</a>
</main>
`;

function serverDocument(): string {
  // The stylesheets the Go pipeline serves, inlined so the iframe needs no
  // network. `?inline` resolves components.css's @imports as esbuild does.
  return `<!doctype html>
<html lang="en" data-theme="light">
<head><meta charset="utf-8"><style>${tokensCss}\n${baseCss}\n${componentsCss}</style></head>
<body>${SERVER_HTML}</body>
</html>`;
}

let frame: HTMLIFrameElement;
let doc: Document;
let win: Window & typeof globalThis;

function load(): Promise<void> {
  frame = document.createElement('iframe');
  frame.style.cssText = 'width:900px;height:700px;border:0';
  frame.srcdoc = serverDocument();
  document.body.append(frame);
  return new Promise((resolve) => {
    frame.addEventListener('load', () => {
      doc = frame.contentDocument as Document;
      win = frame.contentWindow as Window & typeof globalThis;
      resolve();
    });
  });
}

function q<E extends Element = HTMLElement>(selector: string): E {
  const found = doc.querySelector<E>(selector);
  if (!found) throw new Error(`no element matches ${selector}`);
  return found;
}

function style(el: Element): CSSStyleDeclaration {
  return win.getComputedStyle(el);
}

describe('phase 1 — the page before any JavaScript runs', () => {
  // One document for the whole phase: these assertions are about a single
  // page load, and re-parsing between them would prove less, not more.
  beforeAll(load);
  afterAll(() => frame.remove());

  it('has not upgraded anything', () => {
    expect(doc.querySelectorAll('[data-timlee-ready]')).toHaveLength(0);
    expect(win.customElements.get('timlee-button')).toBeUndefined();
  });

  it('shows every component\u2019s content', () => {
    expect(q('timlee-button button').textContent?.trim()).toBe('Create key');
    expect(q('timlee-toast').textContent).toContain('API key created.');
    expect(q('timlee-code-block code').textContent).toContain('api.timlee.dev');
    expect(q('timlee-badge').textContent?.trim()).toBe('Active');
    expect(q('timlee-table tbody').children).toHaveLength(2);
  });

  it('styles the un-upgraded markup from the static stylesheet', () => {
    const submit = q('timlee-button button');
    // The primary variant resolves --button-bg from --ts-primary, so a
    // transparent background would mean the component CSS never applied.
    expect(style(submit).backgroundColor).not.toBe('rgba(0, 0, 0, 0)');
    expect(style(submit).borderRadius).not.toBe('0px');
    // SC 2.5.8: at least 24x24 CSS pixels.
    expect(submit.getBoundingClientRect().height).toBeGreaterThanOrEqual(24);
    expect(submit.getBoundingClientRect().width).toBeGreaterThanOrEqual(24);
  });

  it('submits the form with the real native controls', () => {
    const form = q<HTMLFormElement>('#key-form');
    let submitted: FormData | null = null;
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      submitted = new FormData(form);
    });

    q<HTMLButtonElement>('timlee-button button').click();

    expect(submitted).not.toBeNull();
    const data = submitted as unknown as FormData;
    expect(data.get('name')).toBe('Production');
    expect(data.get('env')).toBe('prod');
    expect(data.get('rotate')).toBe('1');
    expect(data.get('streaming')).toBe('on');
  });

  it('associates labels with their controls', () => {
    const label = q<HTMLLabelElement>('timlee-input label');
    expect(label.control).toBe(q('#key-name'));
    expect(q<HTMLInputElement>('#key-name').labels?.[0]).toBe(label);
  });

  it('keeps the tabs usable as an ordinary table of contents', () => {
    for (const panel of doc.querySelectorAll('[data-tabpanel]')) {
      expect(panel.hasAttribute('hidden')).toBe(false);
      expect((panel as HTMLElement).offsetHeight).toBeGreaterThan(0);
    }
    expect(q<HTMLAnchorElement>('[data-tablist] a').getAttribute('href')).toBe('#panel-usage');
  });

  it('shows the tooltip on focus with no script', async () => {
    const tip = q('#rpm-tip');
    expect(style(tip).visibility).toBe('hidden');

    // The frame itself has to hold focus for :focus-within to match inside it.
    frame.focus();
    win.focus();
    q<HTMLButtonElement>('#rpm-trigger').focus();
    expect(doc.activeElement).toBe(q('#rpm-trigger'));

    // Longer than --ts-duration-fast, so the opacity transition has finished
    // rather than being sampled mid-flight.
    await new Promise<void>((resolve) => setTimeout(resolve, 300));

    // :focus-within on the host is what reveals it; no upgrade involved.
    expect(style(tip).visibility).toBe('visible');
    expect(Number(style(tip).opacity)).toBeGreaterThan(0);
    q<HTMLButtonElement>('#rpm-trigger').blur();
  });

  it('leaves the revoke action as a real link to a real page', () => {
    const trigger = q<HTMLAnchorElement>('#revoke-trigger');
    expect(trigger.getAttribute('href')).toBe('/keys/42/revoke');
    expect(q<HTMLDialogElement>('#revoke dialog').open).toBe(false);
  });
});

describe('phase 2 — the same document after the bundle loads', () => {
  afterAll(() => frame.remove());

  beforeAll(async () => {
    await load();
    const script = doc.createElement('script');
    script.type = 'module';
    script.textContent = `import(${JSON.stringify(LIBRARY_URL)}).then(() => {
      document.documentElement.setAttribute('data-library-loaded', '');
    });`;
    doc.head.append(script);
    await new Promise<void>((resolve, reject) => {
      const started = Date.now();
      const poll = () => {
        if (doc.documentElement.hasAttribute('data-library-loaded')) resolve();
        else if (Date.now() - started > 10_000) reject(new Error('library never loaded'));
        else setTimeout(poll, 20);
      };
      poll();
    });
  });

  it('upgrades every element in place', () => {
    const tags = [
      'timlee-input',
      'timlee-select',
      'timlee-checkbox',
      'timlee-switch',
      'timlee-button',
      'timlee-tabs',
      'timlee-tooltip',
      'timlee-toaster',
      'timlee-toast',
      'timlee-code-block',
      'timlee-table',
      'timlee-badge',
      'timlee-alert',
      'timlee-spinner',
      'timlee-skeleton',
      'timlee-breadcrumb',
      'timlee-pagination',
      'timlee-dialog',
    ];
    for (const tag of tags) {
      expect(q(tag).hasAttribute('data-timlee-ready'), tag).toBe(true);
    }
  });

  it('loses none of the server\u2019s content', () => {
    expect(q('timlee-button button').textContent?.trim()).toBe('Create key');
    expect(q('timlee-code-block code').textContent).toContain('api.timlee.dev');
    expect(q('timlee-toast').textContent).toContain('API key created.');
    expect(q('timlee-table tbody').children).toHaveLength(2);
    expect(q('#key-name').getAttribute('value')).toBe('Production');
  });

  it('still submits the same form data', () => {
    const form = q<HTMLFormElement>('#key-form');
    let submitted: FormData | null = null;
    form.addEventListener('submit', (event) => {
      event.preventDefault();
      submitted = new FormData(form);
    });
    q<HTMLButtonElement>('timlee-button button').click();
    const data = submitted as unknown as FormData | null;
    expect(data?.get('name')).toBe('Production');
    expect(data?.get('streaming')).toBe('on');
  });

  it('wires the ARIA the template could not express', () => {
    const described = q('#key-name').getAttribute('aria-describedby') ?? '';
    expect(described).toContain(q('timlee-input [data-hint]').id);

    const tab = q('[data-tablist] a');
    expect(tab.getAttribute('role')).toBe('tab');
    expect(tab.getAttribute('aria-selected')).toBe('true');
    expect(q('#panel-usage').getAttribute('role')).toBe('tabpanel');
    expect(q('#panel-usage').getAttribute('aria-labelledby')).toBe(tab.id);
    expect(q('#rpm-trigger').getAttribute('aria-describedby')).toBe('rpm-tip');
    expect(q('timlee-spinner').getAttribute('role')).toBe('progressbar');
    expect(q('timlee-spinner').getAttribute('aria-label')).toBe('Loading models');
    expect(q('timlee-breadcrumb nav').getAttribute('aria-label')).toBe('Breadcrumb');
    expect(q('timlee-pagination nav').getAttribute('aria-label')).toBe('Pagination');
  });

  it('switches the tabs to one visible panel with keyboard support', () => {
    const tabs = Array.from(doc.querySelectorAll<HTMLElement>('[role="tab"]'));
    const [first, second] = tabs;
    if (!first || !second) throw new Error('fixture');

    expect(q('#panel-limits').hasAttribute('hidden')).toBe(true);
    expect(first.tabIndex).toBe(0);
    expect(second.tabIndex).toBe(-1);

    first.focus();
    first.dispatchEvent(
      new win.KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }),
    );

    expect(second.getAttribute('aria-selected')).toBe('true');
    expect(q('#panel-usage').hasAttribute('hidden')).toBe(true);
    expect(q('#panel-limits').hasAttribute('hidden')).toBe(false);
  });

  it('turns the revoke link into a modal dialog', () => {
    const dialog = q<HTMLDialogElement>('#revoke dialog');
    q<HTMLAnchorElement>('#revoke-trigger').click();

    expect(dialog.open).toBe(true);
    expect(dialog.matches(':modal')).toBe(true);
    expect(doc.activeElement && dialog.contains(doc.activeElement)).toBe(true);
    // The rest of the page is inert while a modal is open, which is what
    // makes the native focus trap real rather than cosmetic.
    expect(q('#key-name').matches(':focus')).toBe(false);

    dialog.close();
    expect(dialog.open).toBe(false);
  });

  it('adds the controls that only make sense with script', () => {
    expect(q('timlee-toast .timlee-toast__dismiss')).toBeTruthy();
    expect(q('timlee-code-block .timlee-code-block__copy')).toBeTruthy();
    expect(q('timlee-dialog .timlee-dialog__close')).toBeTruthy();
    expect(q('timlee-table th[data-sort] .timlee-table__sort')).toBeTruthy();
  });

  it('sorts the table on the client once it is sortable', () => {
    const header = q<HTMLTableCellElement>('th[data-sort]');
    q<HTMLButtonElement>('th[data-sort] .timlee-table__sort').click();
    expect(header.getAttribute('aria-sort')).toBe('ascending');
    expect(doc.querySelector('tbody tr td')?.textContent).toBe('Production');
  });
});
