/**
 * <timlee-empty-state> and <timlee-skeleton> — the two "nothing here yet"
 * states.
 *
 *   <timlee-empty-state>
 *     <h2>No API keys</h2>
 *     <p>Create a key to start sending requests.</p>
 *     <timlee-button variant="primary"><button>Create key</button></timlee-button>
 *   </timlee-empty-state>
 *
 *   <timlee-skeleton lines="3"></timlee-skeleton>
 *
 * NON-OBVIOUS DECISIONS
 *  - Both are CSS-only apart from registration. A skeleton that needs
 *    JavaScript to appear is useless: the whole point is to fill the gap
 *    before anything has loaded, and a server-rendered page can write one
 *    into the initial HTML.
 *  - The skeleton is `aria-hidden` and carries no text. It is a picture of
 *    content that does not exist yet; announcing it would be announcing
 *    nothing. The region it stands in should carry `aria-busy="true"`.
 *  - The shimmer repeats the `prefers-reduced-motion` guard locally, because
 *    a server-rendered skeleton animates before any element has upgraded and
 *    so cannot rely on the shared `[data-timlee-ready]` hook.
 *  - `lines` draws its rows with a repeating background rather than child
 *    elements, so the count is a number in the markup and the server needs
 *    no loop.
 */

import { define, TimleeElement } from './base';

export class TimleeEmptyState extends TimleeElement {}

export class TimleeSkeleton extends TimleeElement {
  protected override hydrate(): void {
    // It is a placeholder for content that is not there. There is nothing to
    // announce, and announcing the absence would be worse than silence.
    this.setAttribute('aria-hidden', 'true');
  }
}

define('timlee-empty-state', TimleeEmptyState);
define('timlee-skeleton', TimleeSkeleton);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-empty-state': TimleeEmptyState;
    'timlee-skeleton': TimleeSkeleton;
  }
}
