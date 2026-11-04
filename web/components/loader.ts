/**
 * loader.ts — load a component's behaviour only where a page needs it.
 *
 * components.css styles every component before any script runs, and most
 * server-rendered markup needs nothing more: a <timlee-button> around a
 * native <button>, a status <timlee-badge>, a <timlee-empty-state>. An
 * element whose module never loads stays undefined, and an undefined custom
 * element renders its light-DOM children exactly as it would after upgrading,
 * so defining every element on every page bought nothing but a download and
 * an upgrade pass on each load.
 *
 * `loadComponents(root)` looks once through `root` for the markup that does
 * need script — the selectors below — and imports only those modules, each a
 * chunk fetched after the page has rendered. Nothing here watches the DOM:
 *
 *  - an island that creates a component imports its module directly
 *    (`document.createElement('timlee-spinner')` needs './spinner');
 *  - an island that swaps server-rendered markup into the page calls
 *    `loadComponents(container)` on it afterwards.
 */

type Load = () => Promise<unknown>;

/**
 * Markup that cannot work without script, and the module that supplies it.
 * A tag that appears only behind an attribute here is CSS-only without it.
 */
const BEHAVIOURS: ReadonlyArray<readonly [selector: string, load: Load]> = [
  // Label, hint and error wiring, validation state, the textarea's counter.
  ['timlee-input', () => import('./input')],
  ['timlee-textarea', () => import('./textarea')],
  ['timlee-select', () => import('./select')],
  ['timlee-checkbox, timlee-radio', () => import('./choice')],
  ['timlee-switch', () => import('./switch')],
  // Widgets: roles, keyboard, focus and state.
  ['timlee-tabs', () => import('./tabs')],
  ['timlee-dialog', () => import('./dialog')],
  ['timlee-tooltip', () => import('./tooltip')],
  ['timlee-toast, timlee-toaster', () => import('./toast')],
  // A copy button, and a keyboard-reachable region while the content scrolls.
  ['timlee-code-block', () => import('./code-block')],
  ['timlee-table', () => import('./table')],
  // Names and roles the markup leaves to the component.
  ['timlee-avatar, timlee-avatar-group', () => import('./avatar')],
  ['timlee-spinner, timlee-progress', () => import('./spinner')],
  ['timlee-skeleton', () => import('./empty-state')],
  ['timlee-breadcrumb, timlee-pagination', () => import('./navigation')],
  // A card's header and footer would be page landmarks unless the card sits
  // in sectioning content.
  ['timlee-card > header, timlee-card > footer', () => import('./card')],
  // Opt-in behaviour; without the attribute these are CSS-only.
  ['timlee-button[loading], timlee-button[disabled]', () => import('./button')],
  ['timlee-icon-button[label]', () => import('./button')],
  ['timlee-badge[removable]', () => import('./badge')],
  ['timlee-alert[dismissible]', () => import('./alert')],
  ['timlee-chat', () => import('./chat/chat')],
];

const ANY = BEHAVIOURS.map(([selector]) => selector).join(', ');

/** Import the modules `root`'s markup needs. Resolves once they are defined. */
export function loadComponents(root: ParentNode = document): Promise<void> {
  const found = Array.from(root.querySelectorAll(ANY));
  const loads = BEHAVIOURS.filter(([selector]) => found.some((el) => el.matches(selector))).map(
    ([, load]) => load(),
  );
  return Promise.all(loads).then(() => undefined);
}
