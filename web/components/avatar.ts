/**
 * <timlee-avatar> — a person or workspace image with an initials fallback.
 *
 *   <timlee-avatar size="md"><img src="/u/42.webp" alt="Rahul Kumar"></timlee-avatar>
 *   <timlee-avatar name="Timlee Ops"></timlee-avatar>
 *
 * NON-OBVIOUS DECISIONS
 *  - No shadow DOM: the `<img>` is the server's, including its `alt`, its
 *    `srcset` and whatever loading hints the template set. Re-creating it
 *    inside a shadow root would mean re-creating all of that.
 *  - The initials are derived from `alt`/`name` and marked `aria-hidden`,
 *    with the real name carried by the image's `alt` or by an `aria-label`
 *    on the host. Initials are a visual shorthand; "RK" is not a name.
 *  - A broken image is handled by hiding the `<img>` rather than removing it,
 *    so a retry (new `src`) still works and `alt` remains in the tree.
 *  - `<timlee-avatar-group>` overlaps its children with CSS only; the stack
 *    order is DOM order, which keeps the tab order and the visual order the
 *    same for any focusable avatars inside.
 */

import { define, TimleeElement } from './base';

export class TimleeAvatar extends TimleeElement {
  static observedAttributes = ['name', 'status'];

  #initials: HTMLSpanElement | null = null;

  get image(): HTMLImageElement | null {
    return this.querySelector<HTMLImageElement>(':scope > img');
  }

  /** The accessible name: the image's alt, or the `name` attribute. */
  get name(): string {
    return (this.getAttribute('name') ?? this.image?.alt ?? '').trim();
  }

  protected override hydrate(): void {
    const image = this.image;

    if (this.hasAttribute('status') && !this.hasAttribute('aria-label') && this.name) {
      // Colour alone cannot carry the status, so it joins the name.
      this.setAttribute('aria-label', `${this.name}, ${this.getAttribute('status')}`);
    }

    if (!image) {
      this.#showInitials();
      return;
    }

    if (image.complete && image.naturalWidth === 0) this.#fallback();
  }

  protected override connected(signal: AbortSignal): void {
    const image = this.image;
    if (!image) return;
    image.addEventListener('error', () => this.#fallback(), { signal });
    image.addEventListener(
      'load',
      () => {
        image.hidden = false;
        this.#initials?.remove();
        this.#initials = null;
      },
      { signal },
    );
  }

  attributeChangedCallback(): void {
    if (this.ready && this.#initials) this.#initials.textContent = initialsOf(this.name);
  }

  #fallback(): void {
    const image = this.image;
    // Hidden, not removed: a later `src` change can still succeed, and `alt`
    // stays in the accessibility tree as the element's name.
    if (image) image.hidden = true;
    this.#showInitials();
  }

  #showInitials(): void {
    if (this.#initials || (this.textContent ?? '').trim()) return;
    this.#initials = document.createElement('span');
    // "RK" is decoration; the name lives in alt or aria-label.
    this.#initials.setAttribute('aria-hidden', 'true');
    this.#initials.textContent = initialsOf(this.name);
    this.append(this.#initials);
    if (!this.hasAttribute('aria-label') && this.name && !this.image) {
      this.setAttribute('role', 'img');
      this.setAttribute('aria-label', this.name);
    }
  }
}

/** First letters of the first and last word, which is what people expect. */
export function initialsOf(name: string): string {
  const words = name.split(/\s+/).filter(Boolean);
  if (!words.length) return '';
  const first = words[0]?.[0] ?? '';
  const last = words.length > 1 ? (words[words.length - 1]?.[0] ?? '') : '';
  return (first + last).toUpperCase();
}

export class TimleeAvatarGroup extends TimleeElement {
  protected override hydrate(): void {
    // Only a *named* group earns the role; an unnamed one adds an extra layer
    // for a screen reader to walk through and tells it nothing.
    if (this.hasAttribute('aria-label') || this.hasAttribute('aria-labelledby')) {
      this.setAttribute('role', 'group');
    }
  }
}

define('timlee-avatar', TimleeAvatar);
define('timlee-avatar-group', TimleeAvatarGroup);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-avatar': TimleeAvatar;
    'timlee-avatar-group': TimleeAvatarGroup;
  }
}
