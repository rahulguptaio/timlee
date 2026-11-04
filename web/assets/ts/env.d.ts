/**
 * Build-time constants substituted by cmd/timlee-build.
 *
 * These are declarations only; nothing here reaches the bundle. esbuild
 * replaces each identifier with a literal, which lets dead branches be
 * eliminated instead of shipped.
 */

/** True only in a `-dev` build. */
declare const __DEV__: boolean;
