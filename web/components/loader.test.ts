/**
 * loader.test.ts — a page loads the behaviour its markup needs, and no more.
 *
 * Deliberately does not import test-utils, which defines every element: this
 * file's document starts with an empty registry, as a page does, and each
 * test checks what `loadComponents` defined. Imports are cumulative, so the
 * tests run in order from "nothing" to "opted in".
 */

import { afterEach, describe, expect, it } from 'vitest';

import { loadComponents } from './loader';

const defined = (tag: string) => customElements.get(tag) !== undefined;

function mount(html: string): HTMLElement {
  const container = document.createElement('div');
  container.innerHTML = html;
  document.body.append(container);
  return container;
}

afterEach(() => {
  document.body.replaceChildren();
});

describe('loadComponents', () => {
  it('loads nothing for markup that is complete without script', async () => {
    mount(`
      <timlee-button variant="primary"><button type="submit">Save</button></timlee-button>
      <timlee-icon-button><button type="button" aria-label="Menu">≡</button></timlee-icon-button>
      <timlee-badge tone="success" dot>Active</timlee-badge>
      <timlee-alert tone="warning">Approaching your quota.</timlee-alert>
      <timlee-card><p>Created 3 days ago.</p></timlee-card>
      <timlee-empty-state><h2>No API keys</h2></timlee-empty-state>
    `);

    await loadComponents();

    for (const tag of [
      'timlee-button',
      'timlee-icon-button',
      'timlee-badge',
      'timlee-alert',
      'timlee-card',
      'timlee-empty-state',
    ]) {
      expect(defined(tag), tag).toBe(false);
    }
  });

  it('loads only the modules the markup under the root needs', async () => {
    const page = mount(`
      <timlee-tabs>
        <div data-tablist aria-label="Examples">
          <a href="#curl" aria-current="page">curl</a><a href="#python">Python</a>
        </div>
        <section id="curl" data-tabpanel>curl …</section>
        <section id="python" data-tabpanel hidden>python …</section>
      </timlee-tabs>
      <timlee-input><label for="q">Search</label><input id="q" name="q"></timlee-input>
    `);
    mount('<timlee-table><table><tbody><tr><td>1</td></tr></tbody></table></timlee-table>');

    await loadComponents(page);

    expect(defined('timlee-tabs')).toBe(true);
    expect(defined('timlee-input')).toBe(true);
    expect(page.querySelector('[data-tablist] a')?.getAttribute('role')).toBe('tab');
    // Outside the root, and nothing else shares its module.
    expect(defined('timlee-table')).toBe(false);
    expect(defined('timlee-select')).toBe(false);
    expect(defined('timlee-button')).toBe(false);
  });

  it('loads an opt-in behaviour when the markup asks for it', async () => {
    mount(`
      <timlee-alert tone="warning">Rendered with the page.</timlee-alert>
      <timlee-alert dismissible>Maintenance tonight.</timlee-alert>
      <timlee-button loading><button type="submit">Saving</button></timlee-button>
    `);

    await loadComponents();

    expect(defined('timlee-alert')).toBe(true);
    expect(defined('timlee-button')).toBe(true);
    expect(document.querySelector('.timlee-alert__dismiss')).not.toBeNull();
    expect(document.querySelector('timlee-button button')?.hasAttribute('disabled')).toBe(true);
    // The module registered after the document finished loading, yet these
    // alerts were in it already: announcing them would interrupt the reader.
    expect(document.readyState).toBe('complete');
    for (const alert of document.querySelectorAll('timlee-alert')) {
      expect(alert.hasAttribute('role')).toBe(false);
    }
  });

  it('still announces an alert that script inserts once the module is loaded', () => {
    const late = document.createElement('timlee-alert');
    late.setAttribute('tone', 'danger');
    late.textContent = 'Upstream timeout.';
    document.body.append(late);

    expect(late.getAttribute('role')).toBe('alert');
  });
});
