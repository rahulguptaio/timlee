/**
 * <timlee-breadcrumb> and <timlee-pagination> — navigation made of real links.
 *
 *   <timlee-breadcrumb>
 *     <nav aria-label="Breadcrumb">
 *       <ol>
 *         <li><a href="/">Dashboard</a></li>
 *         <li><a href="/keys">API keys</a></li>
 *         <li><a href="/keys/42" aria-current="page">Production</a></li>
 *       </ol>
 *     </nav>
 *   </timlee-breadcrumb>
 *
 *   <timlee-pagination>
 *     <nav aria-label="Pagination">
 *       <a href="?page=1" rel="prev">Previous</a>
 *       <a href="?page=1">1</a>
 *       <a href="?page=2" aria-current="page">2</a>
 *       <a href="?page=3" rel="next">Next</a>
 *     </nav>
 *   </timlee-pagination>
 *
 * NON-OBVIOUS DECISIONS
 *  - Both components are CSS plus a few lines of normalisation. They are
 *    links to URLs a Go handler already serves; paginating on the client
 *    would break sharing, the back button and server-side sorting all at once.
 *  - No roving tabindex. These are lists of ordinary links, and APG's roving
 *    pattern is for composite widgets; applying it here would make every page
 *    number but one unreachable by Tab.
 *  - Separators are `::before` content on list items, so they are not in the
 *    accessibility tree and no screen reader reads "slash" between crumbs.
 *  - The current page is marked with `aria-current="page"`, which is also the
 *    styling hook, so the two can never disagree.
 */

import { t } from '../assets/ts/i18n';
import { define, TimleeElement } from './base';

export class TimleeBreadcrumb extends TimleeElement {
  protected override hydrate(): void {
    const nav = this.querySelector('nav');
    if (nav && !nav.hasAttribute('aria-label') && !nav.hasAttribute('aria-labelledby')) {
      nav.setAttribute(
        'aria-label',
        this.getAttribute('label') ?? t('components.navigation.breadcrumb'),
      );
    }
    // The last crumb is the page you are on; say so if the template did not.
    const links = this.querySelectorAll<HTMLAnchorElement>('a[href]');
    const last = links[links.length - 1];
    if (last && !this.querySelector('[aria-current]')) last.setAttribute('aria-current', 'page');
  }
}

export class TimleePagination extends TimleeElement {
  protected override hydrate(): void {
    const nav = this.querySelector('nav');
    if (nav && !nav.hasAttribute('aria-label') && !nav.hasAttribute('aria-labelledby')) {
      nav.setAttribute(
        'aria-label',
        this.getAttribute('label') ?? t('components.navigation.pagination'),
      );
    }

    for (const link of this.querySelectorAll<HTMLAnchorElement>('a[href]')) {
      if (link.hasAttribute('aria-label')) continue;
      const text = link.textContent?.trim() ?? '';
      // A bare number is meaningless out of context: "3" becomes "Page 3".
      if (!/^\d+$/.test(text)) continue;
      const page = Number(text);
      link.setAttribute(
        'aria-label',
        link.getAttribute('aria-current') === 'page'
          ? t('components.navigation.currentPage', { page })
          : t('components.navigation.page', { page }),
      );
    }
  }

  /** The href of the `rel="next"` link, if the server rendered one. */
  get nextHref(): string | null {
    return this.querySelector<HTMLAnchorElement>('a[rel~="next"]')?.href ?? null;
  }

  get prevHref(): string | null {
    return this.querySelector<HTMLAnchorElement>('a[rel~="prev"]')?.href ?? null;
  }
}

define('timlee-breadcrumb', TimleeBreadcrumb);
define('timlee-pagination', TimleePagination);

declare global {
  interface HTMLElementTagNameMap {
    'timlee-breadcrumb': TimleeBreadcrumb;
    'timlee-pagination': TimleePagination;
  }
}
