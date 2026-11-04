/**
 * Lands in-page links on their target.
 *
 * Sections below the fold skip rendering until they near the viewport
 * (content-visibility), so a jump to one is computed against estimated
 * heights, and a smooth scroll cannot be corrected by scroll anchoring while
 * it runs. Once the scroll has settled, the target is measured and, if it is
 * more than a few pixels from where scroll-padding puts it, scrolled there.
 * A reader who scrolls, types or touches in the meantime is left alone.
 */

const SETTLE_FRAMES = 4;
const GIVE_UP_MS = 3000;

export function landFragments(): void {
  let run = 0;

  const land = () => {
    // A malformed escape (#%E0%A4%A) is still an id someone could have
    // written; decoding it must not throw and take the islands after it down.
    let id = location.hash.slice(1);
    try {
      id = decodeURIComponent(id);
    } catch {
      /* keep it as written */
    }
    const target = id ? document.getElementById(id) : null;
    if (!target) return;

    const ticket = ++run;
    const started = performance.now();
    const listening = new AbortController();
    const finish = () => listening.abort();
    for (const type of ['wheel', 'touchstart', 'keydown', 'pointerdown']) {
      addEventListener(
        type,
        () => {
          run++;
          finish();
        },
        { passive: true, signal: listening.signal },
      );
    }

    let corrections = 0;
    let last = Number.NaN;
    let still = 0;
    const check = () => {
      if (ticket !== run || performance.now() - started > GIVE_UP_MS) {
        finish();
        return;
      }
      const top = target.getBoundingClientRect().top;
      still = Math.abs(top - last) < 0.5 ? still + 1 : 0;
      last = top;
      if (still < SETTLE_FRAMES) {
        requestAnimationFrame(check);
        return;
      }
      const want =
        Number.parseFloat(getComputedStyle(document.documentElement).scrollPaddingTop) || 0;
      if (Math.abs(top - want) <= 4 || corrections === 3) {
        finish();
        return;
      }
      // Instant, so it reads as the end of the same jump rather than a second
      // one; and checked again, since landing renders the sections around it.
      corrections++;
      target.scrollIntoView({ block: 'start', behavior: 'instant' });
      still = 0;
      requestAnimationFrame(check);
    };
    requestAnimationFrame(check);
  };

  addEventListener('hashchange', land);
  // Following a link to the hash already in the address fires no hashchange.
  document.addEventListener('click', (event) => {
    const link = (event.target as Element | null)?.closest<HTMLAnchorElement>('a[href*="#"]');
    if (
      link &&
      link.pathname === location.pathname &&
      link.search === location.search &&
      link.hash === location.hash
    ) {
      requestAnimationFrame(land);
    }
  });
  if (location.hash) land();
}
