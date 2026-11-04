/**
 * Build-time constants substituted by cmd/timlee-build.
 *
 * Mirrors web/assets/ts/env.d.ts so the component sources type-check on their
 * own, without depending on a file another part of the tree owns.
 */

/** True only in a `-dev` build. esbuild replaces it with a literal. */
declare const __DEV__: boolean;
