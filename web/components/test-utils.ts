/**
 * test-utils.ts — the fixtures the component tests share.
 *
 * Importing this module registers every element, exactly as a page importing
 * `web/components/index.ts` would. Tests therefore exercise the same
 * registration path production uses rather than a hand-made one.
 */

import './index';

import baseCss from '../assets/css/base.css?raw';
import tokensCss from '../assets/css/tokens.css?raw';
import componentsCss from './components.css?inline';
import { installEnglishComponentMessages } from './i18n-fixture';

/**
 * The test page is a bare Vitest document, so the stylesheets have to be put
 * there explicitly, in production order — tokens, then base, then the
 * components (`?inline`, so Vite resolves its @imports the way esbuild does)
 * — which is what makes computed-style and colour-contrast assertions mean
 * anything.
 */
if (!document.querySelector('[data-timlee-test-tokens]')) {
  const style = document.createElement('style');
  style.setAttribute('data-timlee-test-tokens', '');
  style.textContent = `${tokensCss}\n${baseCss}\n${componentsCss}`;
  document.head.prepend(style);
}

const containers = new Set<HTMLElement>();

/**
 * Parse `html` the way a server response would and connect it.
 *
 * `innerHTML` is used deliberately: it is the closest thing a test has to the
 * HTML parser, so the elements are created from markup and upgraded on
 * connection, never constructed in JavaScript.
 */
export function mount(html: string): HTMLElement {
  if (!html.includes('id="timlee-i18n"')) installEnglishComponentMessages();
  const container = document.createElement('div');
  container.innerHTML = html;
  document.body.append(container);
  containers.add(container);
  return container;
}

/** Remove everything a test mounted, plus any top-layer leftovers. */
export function cleanup(): void {
  for (const container of containers) container.remove();
  containers.clear();
  for (const node of document.querySelectorAll('[data-timlee-live], timlee-toaster')) node.remove();
  document.querySelector('[data-timlee-test-i18n]')?.remove();
}

export function nextFrame(): Promise<void> {
  return new Promise((resolve) => requestAnimationFrame(() => resolve()));
}

/** Two frames: long enough for a rAF-deferred announcement to land. */
export async function settle(): Promise<void> {
  await nextFrame();
  await nextFrame();
}

/**
 * Take an element out of the document and put it back where it was, twice,
 * as a page that re-renders around a component does. Each trip disconnects
 * and reconnects it; going through a template's content also moves it into
 * another document and back, like settings.ts's swap.
 */
export function moveAround(el: Element): void {
  const parent = el.parentNode;
  const next = el.nextSibling;
  const elsewhere = document.createElement('template').content;
  for (let trip = 0; trip < 2; trip++) {
    elsewhere.append(el);
    parent?.insertBefore(el, next);
  }
}

/** Press a key on an element the way a keyboard would. */
export function press(target: Element, key: string, init: KeyboardEventInit = {}): KeyboardEvent {
  const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(event);
  return event;
}

export function click(target: Element, init: MouseEventInit = {}): MouseEvent {
  const event = new MouseEvent('click', { bubbles: true, cancelable: true, ...init });
  target.dispatchEvent(event);
  return event;
}

/** Collect events of a type until the returned reader is called. */
export function record<T extends Event = Event>(target: EventTarget, type: string): () => T[] {
  const seen: T[] = [];
  target.addEventListener(type, (event) => seen.push(event as T));
  return () => seen;
}

/**
 * Count reads of `scrollWidth`, the measurement that forces a synchronous
 * layout when the page has pending changes. Call `restore()` when done.
 */
export function countScrollWidthReads(): { readonly count: number; restore: () => void } {
  const original = Object.getOwnPropertyDescriptor(Element.prototype, 'scrollWidth');
  const getter = original?.get;
  let count = 0;
  Object.defineProperty(Element.prototype, 'scrollWidth', {
    configurable: true,
    get(this: Element) {
      count++;
      return getter?.call(this) ?? 0;
    },
  });
  return {
    get count() {
      return count;
    },
    restore() {
      if (original) Object.defineProperty(Element.prototype, 'scrollWidth', original);
    },
  };
}

export function $<E extends Element = HTMLElement>(root: ParentNode, selector: string): E {
  const found = root.querySelector<E>(selector);
  if (!found) throw new Error(`no element matches ${selector}`);
  return found;
}
