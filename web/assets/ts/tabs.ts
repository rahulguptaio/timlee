/**
 * Keeps the address in step with a tab chosen in place.
 *
 * Without script a tab is a link the server answers (?view=, ?code=), so the
 * selection lives in the URL. <timlee-tabs> switches panels without that
 * round trip; writing the tab's own parameter back keeps a reload or a copied
 * link on the view the reader chose. Only that parameter: the hrefs were
 * composed when the page was rendered, so the rest of their query may be
 * older than another tab set's choice. replaceState, because a tab is not a
 * page.
 */

export function syncTabsToUrl(root: Document): void {
  root.addEventListener('timlee-tab-change', (event) => {
    const { tab } = (event as CustomEvent<{ tab: HTMLElement }>).detail;
    const param = tab.closest<HTMLElement>('[data-tablist]')?.dataset.param;
    const href = tab.getAttribute('href');
    if (!param || !href) return;

    const value = new URL(href, location.href).searchParams.get(param);
    if (value === null) return;
    const next = new URL(location.href);
    next.searchParams.set(param, value);
    history.replaceState(history.state, '', next);
  });
}
