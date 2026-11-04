import { afterEach, describe, expect, it } from 'vitest';

import type { TimleeTabs } from './tabs';
import { $, cleanup, mount, press, record } from './test-utils';

afterEach(() => {
  cleanup();
  history.replaceState(history.state, '', location.pathname + location.search);
});

const MARKUP = `
  <timlee-tabs>
    <div data-tablist aria-label="Key details">
      <a href="#panel-usage">Usage</a>
      <a href="#panel-limits">Limits</a>
      <a href="#panel-audit">Audit</a>
    </div>
    <section id="panel-usage" data-tabpanel><p>Usage</p></section>
    <section id="panel-limits" data-tabpanel><p>Limits</p></section>
    <section id="panel-audit" data-tabpanel><p>Audit</p></section>
  </timlee-tabs>
`;

function setup(markup = MARKUP) {
  const container = mount(markup);
  const el = $<TimleeTabs>(container, 'timlee-tabs');
  return { container, el, tabs: el.tabs, panels: el.panels };
}

describe('<timlee-tabs>', () => {
  it('promotes links into the APG roles only once script is there', () => {
    const { el, tabs, panels } = setup();

    expect($(el, '[data-tablist]').getAttribute('role')).toBe('tablist');
    expect($(el, '[data-tablist]').getAttribute('aria-orientation')).toBe('horizontal');

    for (const tab of tabs) {
      expect(tab.getAttribute('role')).toBe('tab');
      expect(tab.getAttribute('aria-controls')).toBeTruthy();
    }
    for (const panel of panels) {
      expect(panel.getAttribute('role')).toBe('tabpanel');
      expect(panel.getAttribute('aria-labelledby')).toBeTruthy();
      // APG: the panel is focusable so content with no controls is reachable.
      expect(panel.tabIndex).toBe(0);
    }
  });

  it('shows exactly one panel', () => {
    const { panels } = setup();
    expect(panels.filter((p) => !p.hidden)).toHaveLength(1);
    expect(panels[0]?.hidden).toBe(false);
  });

  it('keeps one tab in the tab order and moves it with the arrows', () => {
    const { tabs } = setup();
    const [first, second, third] = tabs;
    if (!first || !second || !third) throw new Error('fixture');

    expect(tabs.map((t) => t.tabIndex)).toEqual([0, -1, -1]);

    press(first, 'ArrowRight');
    expect(second.getAttribute('aria-selected')).toBe('true');
    expect(tabs.map((t) => t.tabIndex)).toEqual([-1, 0, -1]);
    expect(document.activeElement).toBe(second);

    press(second, 'End');
    expect(third.getAttribute('aria-selected')).toBe('true');

    // Wrapping is what APG expects of a tab list.
    press(third, 'ArrowRight');
    expect(first.getAttribute('aria-selected')).toBe('true');

    press(first, 'Home');
    expect(first.getAttribute('aria-selected')).toBe('true');
  });

  it('uses the vertical arrows when the orientation says vertical', () => {
    const { el, tabs } = setup(
      MARKUP.replace('<timlee-tabs>', '<timlee-tabs orientation="vertical">'),
    );
    const [first, second] = tabs;
    if (!first || !second) throw new Error('fixture');

    expect($(el, '[data-tablist]').getAttribute('aria-orientation')).toBe('vertical');
    press(first, 'ArrowRight');
    expect(first.getAttribute('aria-selected')).toBe('true');
    press(first, 'ArrowDown');
    expect(second.getAttribute('aria-selected')).toBe('true');
  });

  it('follows the visual inline direction in RTL', () => {
    const { el, tabs } = setup();
    el.dir = 'rtl';
    const [first, second, third] = tabs;
    if (!first || !second || !third) throw new Error('fixture');

    press(first, 'ArrowLeft');
    expect(second.getAttribute('aria-selected')).toBe('true');

    press(second, 'ArrowRight');
    expect(first.getAttribute('aria-selected')).toBe('true');

    press(first, 'ArrowRight');
    expect(third.getAttribute('aria-selected')).toBe('true');
  });

  it('waits for Enter or Space when activation is manual', () => {
    const { tabs } = setup(MARKUP.replace('<timlee-tabs>', '<timlee-tabs manual>'));
    const [first, second] = tabs;
    if (!first || !second) throw new Error('fixture');

    press(first, 'ArrowRight');
    expect(document.activeElement).toBe(second);
    expect(second.getAttribute('aria-selected')).toBe('false');

    press(second, 'Enter');
    expect(second.getAttribute('aria-selected')).toBe('true');
  });

  it('cancels the anchor jump and switches panels instead', () => {
    const { tabs, panels } = setup();
    const second = tabs[1];
    if (!second) throw new Error('fixture');

    const event = new MouseEvent('click', { bubbles: true, cancelable: true });
    second.dispatchEvent(event);

    expect(event.defaultPrevented).toBe(true);
    expect(panels[0]?.hidden).toBe(true);
    expect(panels[1]?.hidden).toBe(false);
    // The href survives as the scriptless fallback.
    expect(second.getAttribute('href')).toBe('#panel-limits');
  });

  it('announces the change but leaves the URL alone by default', () => {
    const { el, tabs } = setup();
    const events = record<CustomEvent<{ panelId: string }>>(el, 'timlee-tab-change');
    const before = location.hash;

    tabs[1]?.click();

    expect(events()[0]?.detail.panelId).toBe('panel-limits');
    // Rewriting the page URL on every tab click would fight with any router,
    // and two tab sets on one page would fight each other.
    expect(location.hash).toBe(before);
  });

  it('writes the hash without a history entry when sync-hash is set', () => {
    const { tabs } = setup(MARKUP.replace('<timlee-tabs>', '<timlee-tabs sync-hash>'));
    const depth = history.length;

    tabs[1]?.click();

    expect(location.hash).toBe('#panel-limits');
    expect(history.length).toBe(depth);
  });

  it('selects the tab named by the hash on load', () => {
    history.replaceState(history.state, '', '#panel-audit');
    const { tabs, panels } = setup();
    expect(tabs[2]?.getAttribute('aria-selected')).toBe('true');
    expect(panels[2]?.hidden).toBe(false);
  });

  it('honours aria-current from the server as the initial selection', () => {
    const { tabs, panels } = setup(
      MARKUP.replace('<a href="#panel-limits">', '<a href="#panel-limits" aria-current="page">'),
    );
    expect(tabs[1]?.getAttribute('aria-selected')).toBe('true');
    expect(panels[1]?.hidden).toBe(false);
    // aria-current was the scriptless marker; aria-selected supersedes it.
    expect(tabs[1]?.hasAttribute('aria-current')).toBe(false);
  });

  it('selects by panel id through the value property', () => {
    const { el, panels } = setup();
    el.value = 'panel-audit';
    expect(el.value).toBe('panel-audit');
    expect(panels[2]?.hidden).toBe(false);
  });

  it('does not steal the panels of a nested tab set', () => {
    const container = mount(`
      <timlee-tabs id="outer">
        <div data-tablist><a href="#outer-a">A</a></div>
        <section id="outer-a" data-tabpanel>
          <timlee-tabs id="inner">
            <div data-tablist><a href="#inner-a">A</a></div>
            <section id="inner-a" data-tabpanel>inner</section>
          </timlee-tabs>
        </section>
      </timlee-tabs>
    `);
    const outer = $<TimleeTabs>(container, '#outer');
    const inner = $<TimleeTabs>(container, '#inner');
    expect(outer.tabs).toHaveLength(1);
    expect(inner.tabs).toHaveLength(1);
    expect(outer.tabs[0]?.getAttribute('aria-controls')).toBe('outer-a');
  });
});
