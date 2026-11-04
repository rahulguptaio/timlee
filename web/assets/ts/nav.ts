/**
 * The popover menus: the site menu, the account panel, the docs sidebar.
 *
 * Each is a popover its button opens, so the browser already supplies Escape,
 * closing on a click outside, focus returning to the button, and Tab moving
 * from the button into the menu. What is left for script: saying on every
 * invoker whether its popover is open, the same in every browser rather than
 * only where the invoker maps it implicitly; and, for a menu, closing it when
 * a link inside it is followed without unloading the page, or when the
 * window widens past the breakpoint where it would otherwise sit invisible
 * in the top layer, or leave its backdrop over the page.
 */

export function syncExpanded(root: Document): void {
  for (const popover of root.querySelectorAll<HTMLElement>('[popover][id]')) {
    const invokers = root.querySelectorAll(
      `[popovertarget="${popover.id}"]:not([popovertargetaction="hide"])`,
    );
    const sync = (open: boolean) => {
      for (const invoker of invokers) invoker.setAttribute('aria-expanded', String(open));
    };
    sync(false);
    popover.addEventListener('toggle', (event) => sync((event as ToggleEvent).newState === 'open'));
  }
}

export function enhanceMenu(menu: HTMLElement): void {
  const close = () => {
    if (menu.matches(':popover-open')) menu.hidePopover();
  };
  menu.addEventListener('click', (event) => {
    if ((event.target as Element | null)?.closest('a[href]')) close();
  });

  const wide = matchMedia('(width >= 64rem)');
  wide.addEventListener('change', () => {
    if (wide.matches) close();
  });
}
