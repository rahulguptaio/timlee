/**
 * <timlee-code-block> — a readable, copyable block of code.
 *
 *   <timlee-code-block label="curl example" language="bash">
 *     <pre><code>curl https://api.timlee.dev/v1/chat/completions \
 *   -H "Authorization: Bearer $TIMLEE_API_KEY"</code></pre>
 *   </timlee-code-block>
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM: the code is the page's content. It must be findable with
 *    Ctrl-F, selectable, and indexable, and a shadow root makes browser find
 *    unreliable across engines.
 *  - The copy button is injected rather than server-rendered, because
 *    `navigator.clipboard` is the only thing it can do and that needs script.
 *    The code is fully readable and selectable without it.
 *  - `<pre>` gets `tabindex="0"` and a labelled `role="region"` only when the
 *    code actually overflows. A scrollable area that keyboard users cannot
 *    scroll fails WCAG 2.1.1; a tab stop on every short snippet is noise.
 *    The exception is a block inside off-screen `content-visibility: auto`
 *    content, which cannot be measured until it renders: it is offered as a
 *    region meanwhile, and measured when the reader gets there.
 *  - Success is announced through the shared polite live region, so focus
 *    stays put. The button's own label does change to "Copied", and its
 *    `aria-label` changes with it: WCAG 2.5.3 requires the visible text to be
 *    part of the accessible name, so "Copied" + `aria-label="Copy install"`
 *    would be a violation rather than a clever way to keep the name stable.
 */

import { t } from '../assets/ts/i18n';
import { distinctName, whenSkipped } from './a11y';
import { announce } from './announce';
import { define, ensureId, TimleeElement } from './base';

/** What a code block is called; numbered by distinctName when it repeats. */
function codeBlockName(el: Element): string {
  return el.getAttribute('label') ?? el.getAttribute('language') ?? t('components.code.code');
}

export class TimleeCodeBlock extends TimleeElement {
  #copyButton: HTMLButtonElement | null = null;
  #resetTimer = 0;
  /** True once the observer has reported this connection's layout. */
  #measured = false;
  /** True when the region's aria-label is the one this element gave it. */
  #named = false;

  get pre(): HTMLPreElement | null {
    return this.querySelector<HTMLPreElement>(':scope > pre');
  }

  /** The code, exactly as the server wrote it. */
  get code(): string {
    return this.pre?.textContent ?? '';
  }

  protected override hydrate(): void {
    const pre = this.pre;
    if (!pre) return;

    // `<header>` in a custom element is a `banner` landmark; a code sample is
    // not the page's banner. See the same note in card.ts.
    const header = this.querySelector(':scope > header');
    if (header && !header.hasAttribute('role')) header.setAttribute('role', 'none');

    if (!this.hasAttribute('no-copy') && navigator.clipboard) {
      this.#copyButton = document.createElement('button');
      this.#copyButton.type = 'button';
      this.#copyButton.className = 'timlee-code-block__copy';
      this.#setCopyLabel('idle');
      this.append(this.#copyButton);
    }
  }

  protected override connected(signal: AbortSignal): void {
    this.#copyButton?.addEventListener('click', () => void this.copy(), { signal });
    const pre = this.pre;
    if (!pre) return;
    // As in table.ts: the observer reports after layout, so nothing here
    // forces one during startup.
    if (typeof ResizeObserver !== 'function') {
      this.#syncScrollRegion(pre);
      return;
    }
    this.#measured = false;
    const observer = new ResizeObserver(() => {
      this.#measured = true;
      this.#syncScrollRegion(pre);
    });
    observer.observe(pre);
    signal.addEventListener('abort', () => observer.disconnect(), { once: true });
    whenSkipped(
      pre,
      () => this.#measured,
      () => this.#markScrollRegion(pre, true),
      signal,
    );
  }

  protected override disconnected(): void {
    clearTimeout(this.#resetTimer);
  }

  /**
   * Keep the visible text and the accessible name in step. SC 2.5.3 (Label in
   * Name) requires the one to be contained in the other, so both change
   * together or neither does.
   */
  #setCopyLabel(state: 'idle' | 'done'): void {
    const button = this.#copyButton;
    if (!button) return;
    const idle = state === 'idle';
    const visible = idle ? t('components.code.copy') : t('components.code.copied');
    const what = this.getAttribute('label');
    button.textContent = visible;
    let label = idle ? t('components.code.copyCode') : t('components.code.copiedCode');
    if (what) {
      label = idle
        ? t('components.code.copyLabel', { label: what })
        : t('components.code.copiedLabel', { label: what });
    }
    button.setAttribute('aria-label', label);
  }

  #syncScrollRegion(pre: HTMLPreElement): void {
    this.#markScrollRegion(pre, pre.scrollWidth > pre.clientWidth + 1);
  }

  #markScrollRegion(pre: HTMLPreElement, scrolls: boolean): void {
    if (scrolls) {
      pre.setAttribute('role', 'region');
      pre.setAttribute('tabindex', '0');
      if (!pre.hasAttribute('aria-label')) {
        const name = distinctName(this, 'timlee-code-block', codeBlockName);
        pre.setAttribute('aria-label', t('components.code.scrollable', { label: name }));
        this.#named = true;
      }
      ensureId(pre, 'timlee-code');
      return;
    }
    // A block offered before it could be measured (see whenSkipped) keeps its
    // tab stop while it has focus, rather than drop focus out from under the
    // keyboard; it is measured again when focus moves on.
    if (pre === document.activeElement) {
      pre.addEventListener('blur', () => this.#syncScrollRegion(pre), { once: true });
      return;
    }
    pre.removeAttribute('role');
    pre.removeAttribute('tabindex');
    // A name without the region role is a name on nothing (aria-label is
    // prohibited on a plain <pre>), so the one given here goes with it.
    if (this.#named) {
      pre.removeAttribute('aria-label');
      this.#named = false;
    }
  }

  /** Copy to the clipboard and announce it. Resolves false if it failed. */
  async copy(): Promise<boolean> {
    try {
      await navigator.clipboard.writeText(this.code);
    } catch {
      announce(t('components.code.copyFailed'), 'assertive');
      this.emit('timlee-copy', { ok: false });
      return false;
    }

    // Immediate confirmation goes to the shared polite region; the button
    // shows the same thing visually and renames itself to match.
    announce(this.getAttribute('copied-label') ?? t('components.code.copiedToClipboard'));
    if (this.#copyButton) {
      this.#copyButton.setAttribute('data-copied', '');
      this.#setCopyLabel('done');
      clearTimeout(this.#resetTimer);
      this.#resetTimer = window.setTimeout(() => {
        this.#copyButton?.removeAttribute('data-copied');
        this.#setCopyLabel('idle');
      }, 2000);
    }
    this.emit('timlee-copy', { ok: true });
    return true;
  }
}

define('timlee-code-block', TimleeCodeBlock);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-code-block': TimleeCodeBlock;
  }
}
