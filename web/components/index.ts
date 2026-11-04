/**
 * index.ts — the whole Timlee web component library in one import.
 *
 * Importing this module defines all 26 custom elements.
 *
 * WHAT THE GO SIDE NEEDS
 *
 *   1. One line in `web/assets/app.css`:
 *
 *        @import "../components/components.css";
 *
 *      With it, a server-rendered page is fully styled before any JavaScript
 *      runs and stays styled if the bundle never arrives. It is also the only
 *      way the component styles reach a page: nothing injects them at
 *      runtime, which the page CSP would refuse anyway.
 *
 *   2. A script entry to load the behaviour — `cmd/timlee-build` treats
 *      files directly inside `web/assets/` as entries and everything nested
 *      as a module. A server-rendered page loads only what its markup needs
 *      (see loader.ts and web/assets/app.ts):
 *
 *        import { loadComponents } from '../components/loader';
 *        void loadComponents();
 *
 *      Importing this module instead defines every element at once, which
 *      is what Storybook and the component tests do:
 *
 *        import '../components';
 *
 *   3. Nothing else. No per-template registration, no `<script>` per
 *      component, no manifest. `html/template` writes ordinary HTML and the
 *      elements upgrade it in place.
 */

import './button';
import './field';
import './input';
import './textarea';
import './select';
import './choice';
import './switch';
import './card';
import './badge';
import './alert';
import './avatar';
import './spinner';
import './empty-state';
import './navigation';
import './table';
import './code-block';
import './tabs';
import './tooltip';
import './dialog';
import './toast';

export { RovingTabindex } from './a11y';
export { type AlertTone, TimleeAlert } from './alert';
export { announce } from './announce';
export { initialsOf, TimleeAvatar, TimleeAvatarGroup } from './avatar';
export { TimleeBadge } from './badge';
export { define, ensureId, prefersReducedMotion, READY_ATTR, TimleeElement, uid } from './base';
export { TimleeButton, TimleeIconButton } from './button';
export { TimleeCard } from './card';
export { TimleeCheckbox, TimleeRadio } from './choice';
export { TimleeCodeBlock } from './code-block';
export { TimleeDialog } from './dialog';
export { TimleeEmptyState, TimleeSkeleton } from './empty-state';
export { type FieldControl, TimleeField } from './field';
export { TimleeInput } from './input';
export { TimleeBreadcrumb, TimleePagination } from './navigation';
export { TimleeSelect } from './select';
export { TimleeProgress, TimleeSpinner } from './spinner';
export { TimleeSwitch } from './switch';
export { TimleeTable } from './table';
export { TimleeTabs } from './tabs';
export { TimleeTextarea } from './textarea';
export { TimleeToast, TimleeToaster, type ToastOptions, type ToastTone } from './toast';
export { TimleeTooltip } from './tooltip';

/** Every tag this module defines, in registration order. */
export const TIMLEE_TAGS = [
  'timlee-button',
  'timlee-icon-button',
  'timlee-input',
  'timlee-textarea',
  'timlee-select',
  'timlee-checkbox',
  'timlee-radio',
  'timlee-switch',
  'timlee-card',
  'timlee-badge',
  'timlee-alert',
  'timlee-avatar',
  'timlee-avatar-group',
  'timlee-spinner',
  'timlee-progress',
  'timlee-empty-state',
  'timlee-skeleton',
  'timlee-breadcrumb',
  'timlee-pagination',
  'timlee-table',
  'timlee-code-block',
  'timlee-tabs',
  'timlee-tooltip',
  'timlee-dialog',
  'timlee-toast',
  'timlee-toaster',
] as const;

export type TimleeTag = (typeof TIMLEE_TAGS)[number];
