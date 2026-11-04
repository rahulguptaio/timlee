/**
 * base.ts — the shared substrate for every Timlee custom element.
 *
 * ============================================================================
 * ADR-001: these components do not use shadow DOM.
 * ============================================================================
 * Timlee's HTML is produced by Go `html/template`. The server emits the real
 * markup; a custom element only *upgrades* what is already on the page. Three
 * properties of shadow DOM are in direct conflict with that:
 *
 *  1. Server-rendered content. Anything the Go template writes between the
 *     tags lands in the light DOM. A shadow root hides it unless every
 *     component ships a `<slot>` map that the template has to match, or the
 *     template inlines a `<template shadowrootmode>` — which couples the
 *     server to each component's private internals. Declarative shadow DOM
 *     (Baseline 2024: Chrome 111, Safari 16.4, Firefox 123) solves *visibility*
 *     but not points 2 and 3, and it is the coupling that disqualifies it.
 *
 *  2. Cross-root ARIA. `aria-labelledby`, `aria-describedby`, `aria-controls`
 *     and `<label for>` only resolve inside one tree. Tabs, dialogs, tooltips
 *     and every labelled form field depend on them. The fix — ARIA Reference
 *     Target — is not shipped anywhere as of this writing, so a shadow
 *     boundary here means silently broken screen-reader semantics.
 *
 *  3. Form participation. A native `<input>` in the light DOM is submitted,
 *     validated, reset and autofilled for free. Re-creating that behind a
 *     shadow root means `ElementInternals` plus hand-written validity, reset
 *     and state-restore plumbing for every field.
 *
 * What shadow DOM buys — style encapsulation — is bought here instead by a
 * single prefixed stylesheet whose every selector is anchored to a
 * `timlee-*` tag name (see components.css). The trade is deliberate and documented
 * per component in each file's header.
 *
 * `ElementInternals` *is* used, without a shadow root, by `<timlee-switch>`
 * when it has no native checkbox to drive: a form-associated custom element
 * does not require shadow DOM, only `static formAssociated = true`.
 *
 * ============================================================================
 * Upgrade timing
 * ============================================================================
 * `connectedCallback` can run while the parser is still streaming children in,
 * which would make an element hydrate against half its content. `whenParsed`
 * below defers until the document is past `loading` whenever that risk exists.
 */

/** Set on an element once its JavaScript enhancement has run. */
export const READY_ATTR = 'data-timlee-ready';

type Task = () => void;

const pending: Task[] = [];
let scheduled = false;

function flush(): void {
  scheduled = false;
  const tasks = pending.splice(0, pending.length);
  for (const task of tasks) {
    try {
      task();
    } catch (error) {
      reportError(error);
    }
  }
}

/**
 * Run `task` once the document's children are guaranteed to be complete.
 *
 * The production bundle is a deferred module, so `readyState` is already past
 * `loading` and this is a straight call. It matters for an `async`-loaded
 * bundle, for `document.write` streaming, and for tests that define elements
 * before the fixture has finished parsing.
 */
export function whenParsed(task: Task): void {
  if (document.readyState !== 'loading') {
    task();
    return;
  }
  pending.push(task);
  if (!scheduled) {
    scheduled = true;
    document.addEventListener('DOMContentLoaded', flush, { once: true });
  }
}

function reportError(error: unknown): void {
  // Never let one broken element stop the rest of the page from upgrading.
  queueMicrotask(() => {
    throw error;
  });
}

let counter = 0;

/** A document-unique id, used to wire ARIA relationships the server omitted. */
export function uid(prefix: string): string {
  let id: string;
  do {
    id = `${prefix}-${(++counter).toString(36)}`;
  } while (document.getElementById(id));
  return id;
}

/** Return the element's id, assigning a generated one if it has none. */
export function ensureId(el: Element, prefix: string): string {
  if (!el.id) el.id = uid(prefix);
  return el.id;
}

/** Add `token` to a space-separated attribute without disturbing what is there. */
export function addToken(el: Element, attr: string, token: string): void {
  const current = (el.getAttribute(attr) ?? '').split(/\s+/).filter(Boolean);
  if (!current.includes(token)) {
    current.push(token);
    el.setAttribute(attr, current.join(' '));
  }
}

export function removeToken(el: Element, attr: string, token: string): void {
  const current = (el.getAttribute(attr) ?? '').split(/\s+/).filter(Boolean);
  const next = current.filter((t) => t !== token);
  if (next.length) el.setAttribute(attr, next.join(' '));
  else el.removeAttribute(attr);
}

/**
 * Mirror a boolean property onto its attribute, which is what CSS can see.
 *
 * The no-op guard is load-bearing, not an optimisation: `setAttribute` queues
 * an `attributeChangedCallback` even when the value is unchanged, so a
 * component that reflects from inside that callback would recurse forever.
 */
export function reflectBool(el: Element, attr: string, value: boolean): void {
  if (value === el.hasAttribute(attr)) return;
  if (value) el.setAttribute(attr, '');
  else el.removeAttribute(attr);
}

export function reflectString(el: Element, attr: string, value: string | null): void {
  const empty = value === null || value === '';
  if (empty) {
    if (el.hasAttribute(attr)) el.removeAttribute(attr);
    return;
  }
  if (el.getAttribute(attr) !== value) el.setAttribute(attr, value);
}

/** Focusable descendants in tab order, skipping anything inert or hidden. */
const FOCUSABLE =
  'a[href],area[href],button,input,select,textarea,summary,iframe,object,embed,' +
  'audio[controls],video[controls],[contenteditable]:not([contenteditable="false"]),[tabindex]';

export function focusableWithin(root: ParentNode): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter(isFocusable);
}

export function isFocusable(el: HTMLElement): boolean {
  if (el.hasAttribute('disabled') || el.getAttribute('aria-hidden') === 'true') return false;
  if (el.tabIndex < 0) return false;
  if (el.closest('[inert]')) return false;
  // offsetParent is null for `display:none` subtrees; `position:fixed`
  // elements report null too, hence the rect fallback.
  if (el.offsetParent === null && el.getClientRects().length === 0) return false;
  return true;
}

/**
 * Base class. Subclasses implement `hydrate` (runs once, builds whatever the
 * server could not) and optionally `connected`/`disconnected`.
 *
 * `connected` runs on every insertion and its signal aborts when the element
 * leaves the document, so moving a component around the DOM — which
 * Go-rendered pages do on every htmx-style swap — neither leaks a handler nor
 * loses one. Listeners therefore belong in `connected`: `hydrate` runs once,
 * and anything it attached would be gone after the first move.
 */
export abstract class TimleeElement extends HTMLElement {
  #hydrated = false;
  #controller: AbortController | null = null;

  /** True once `hydrate()` has run. Tests and sibling components read this. */
  get ready(): boolean {
    return this.#hydrated;
  }

  connectedCallback(): void {
    this.#controller = new AbortController();
    const signal = this.#controller.signal;

    if (this.#hydrated) {
      this.connected(signal);
      return;
    }

    whenParsed(() => {
      if (!this.isConnected || this.#hydrated) return;
      this.#hydrated = true;
      this.hydrate();
      this.setAttribute(READY_ATTR, '');
      this.connected(this.#controller?.signal ?? signal);
    });
  }

  disconnectedCallback(): void {
    this.#controller?.abort();
    this.#controller = null;
    this.disconnected();
  }

  /** Build-once work: generate ids, promote roles, inject controls. No listeners. */
  protected hydrate(): void {}

  /** Per-connection work. Attach listeners with `{ signal }`. */
  protected connected(_signal: AbortSignal): void {}

  protected disconnected(): void {}

  /** Dispatch a cancelable, bubbling, composed component event. */
  protected emit<T>(type: string, detail?: T, options: EventInit = {}): boolean {
    return this.dispatchEvent(
      new CustomEvent(type, {
        bubbles: true,
        composed: true,
        cancelable: true,
        ...options,
        detail: detail as T,
      }),
    );
  }
}

/**
 * Register a custom element, tolerating a double import. Returns the
 * constructor already registered under that name when there is one, so a
 * second bundle on the page cannot throw during module evaluation.
 *
 * A no-op where there is no custom element registry, such as a worker.
 */
export function define<T extends CustomElementConstructor>(tag: string, ctor: T): T {
  if (typeof customElements === 'undefined') return ctor;
  const existing = customElements.get(tag);
  if (existing) return existing as T;
  customElements.define(tag, ctor);
  return ctor;
}

/** True when the user has asked for less motion. */
export function prefersReducedMotion(): boolean {
  return window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? false;
}
