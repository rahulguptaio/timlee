import { describe, expect, it } from 'vitest';

// `?inline` resolves the @imports, as esbuild does for the shipped stylesheet.
import checkedIn from './components.css?inline';
import index from './components.css?raw';
import { TIMLEE_TAGS } from './index';

describe('the component stylesheet', () => {
  it('imports every component stylesheet, shared rules first', () => {
    const imported = [...index.matchAll(/^@import "\.\/([a-z-]+\.css)";$/gm)].map((m) => m[1]);
    const onDisk = Object.keys(import.meta.glob('./*.css'))
      .map((path) => path.slice(2))
      .filter((name) => name !== 'components.css');

    expect([...imported].sort()).toEqual(onDisk.sort());
    expect(imported[0]).toBe('shared.css');
  });

  it('styles every tag the library defines', () => {
    for (const tag of TIMLEE_TAGS) {
      // timlee-avatar-group and timlee-radio are styled through grouped
      // selectors, so a substring check is the honest assertion here.
      expect(checkedIn, tag).toContain(tag);
    }
  });

  it('routes semantic tokens through component-local properties', () => {
    const authored = checkedIn.replace(/\/\*[\s\S]*?\*\//g, '');
    const declarations = [...authored.matchAll(/(?:^|[;{])\s*([-\w]+)\s*:\s*([^;{}]+);/gm)];
    const offenders = declarations
      .map(([, property = '', value = '']) => [property, value.trim()] as const)
      .filter(([property, value]) => !property.startsWith('--') && /var\(--ts-/.test(value))
      .map(([property, value]) => `${property}: ${value}`);

    expect(offenders).toEqual([]);
  });

  it('never hardcodes a colour outside a forced-colors block', () => {
    const withoutComments = checkedIn.replace(/\/\*[\s\S]*?\*\//g, '');
    const forcedColors = withoutComments.split('@media (forced-colors: active)').slice(1).join('');
    const authored = withoutComments.replace(forcedColors, '');

    // Hex literals, rgb()/hsl() functions and named colours all belong in
    // tokens.css, never in a component.
    expect(authored).not.toMatch(/#[0-9a-f]{3,8}\b/i);
    expect(authored).not.toMatch(/\b(?:rgb|rgba|hsl|hsla)\(/i);
  });

  it('sources every colour-ish custom property from a --ts-* token', () => {
    const declarations = checkedIn.match(/--[a-z0-9-]+:\s*[^;]+;/g) ?? [];

    // A component property may be defined in terms of another component
    // property (`--switch-track: var(--switch-track-on)`), so the check walks
    // the chain and only demands that it *ends* at a semantic token.
    const byName = new Map<string, string[]>();
    for (const decl of declarations) {
      const [name = '', value = ''] = decl.split(/:(.*)/s);
      const key = name.trim();
      byName.set(key, [...(byName.get(key) ?? []), value]);
    }

    const SAFE =
      /var\(--ts-|currentcolor|transparent|inherit|none|ButtonFace|ButtonText|ButtonBorder|Highlight|HighlightText|Canvas/i;

    function resolvesToToken(value: string, depth = 0): boolean {
      if (SAFE.test(value)) return true;
      if (depth > 5) return false;
      const refs = [...value.matchAll(/var\((--[a-z0-9-]+)/gi)].map((m) => m[1] ?? '');
      return refs.some((ref) => (byName.get(ref) ?? []).some((v) => resolvesToToken(v, depth + 1)));
    }

    const offenders = declarations.filter((decl) => {
      const [name = '', value = ''] = decl.split(/:(.*)/s);
      if (name.trim().startsWith('--ts-')) return false;
      if (!/(bg|fg|colou?r|border|accent|track|thumb|indicator|ring|sheen|base|dot)/.test(name)) {
        return false;
      }
      return !resolvesToToken(value);
    });
    expect(offenders).toEqual([]);
  });

  it('lifts a surface only with an elevation token', () => {
    // --ts-elevation-* carry the inset highlight a raised surface needs in
    // dark; a bare --ts-shadow-* or a literal shadow would leave it out. A
    // shadow is an elevation token (directly or through the component's own
    // --*-shadow property), none, or a ring with no offset and no blur.
    const authored = checkedIn.replace(/\/\*[\s\S]*?\*\//g, '');
    expect(authored).not.toMatch(/var\(--ts-shadow-/);
    expect(authored).not.toMatch(/var\(--ts-highlight\)/);

    const declarations = [...authored.matchAll(/(box-shadow|--[a-z0-9-]+-shadow)\s*:\s*([^;]+);/g)];
    expect(declarations.length).toBeGreaterThan(0);
    const offenders = declarations
      .map(([, property = '', value = '']) => [property, value.trim()] as const)
      .filter(([property, value]) => {
        if (value === 'none' || /^var\(--(?:mc|ts)-elevation-[1-4]\)$/.test(value)) return false;
        if (property === 'box-shadow' && /^var\(--[a-z-]+-shadow\)$/.test(value)) return false;
        return !(property === 'box-shadow' && /^0 0 0 [^,]+$/.test(value));
      })
      .map(([property, value]) => `${property}: ${value}`);
    expect(offenders).toEqual([]);
  });

  it('states reduced motion beside the motion, never as a global switch', () => {
    // A blanket 0.01ms duration freezes indicators mid-frame instead of
    // calming them; each moving rule carries its own reduced form instead.
    const authored = checkedIn.replace(/\/\*[\s\S]*?\*\//g, '');
    expect(authored).not.toMatch(/0\.01ms/);
    expect(authored).not.toMatch(/\[data-timlee-ready\]\s*\*/);

    const reducedBlocks = authored.split('@media (prefers-reduced-motion: reduce)').slice(1);
    const joined = reducedBlocks.join('\n');
    for (const indicator of ['timlee-skeleton', 'timlee-spinner', '.timlee-button__spinner']) {
      expect(joined, indicator).toContain(indicator);
    }
  });
});
