# Timlee design tokens

Every colour, size, duration and shadow in the product comes from
`web/assets/css/tokens.css`; document defaults and the handful of shared
utilities live in `web/assets/css/base.css`. Both are plain CSS custom
properties, bundled into `app.css` by `go run ./cmd/timlee-build`. There is no
preprocessor and no theme object in JavaScript.

`/design` is the rendered reference for this contract. Its swatches resolve
these custom properties in the browser, including nested light and dark
examples; it contains no copied colour values or reference-only token map.

- [How it is built](#how-it-is-built)
- [Light and dark parity](#light-and-dark-parity)
- [Colour ramps](#colour-ramps)
- [The role model](#the-role-model)
- [Semantic reference](#semantic-reference)
- [Measured contrast](#measured-contrast)
- [Elevation](#elevation)
- [Type](#type)
- [Space and rhythm](#space-and-rhythm)
- [Radius](#radius)
- [Motion](#motion)
- [Layering](#layering)
- [Aliases](#aliases)
- [Rules for consumers](#rules-for-consumers)
- [Verifying a change](#verifying-a-change)

---

## How it is built

Three layers, and the direction is one-way:

| Layer | Prefix | Holds | May reference |
|---|---|---|---|
| 1 Primitive | `--prim-*` | literal colours: twelve-step ramps, washes, shadow inks | nothing |
| 2 Semantic | `--ts-*` | a job ("page background", "danger ink"), the same name in both themes | layer 1 |
| 3 Component | `--teal`, `--chat-*`, a widget's own `--button-*` | one widget's local contract | layer 2 only |

**A literal colour may appear only in layer 1.** Layers 2 and 3 are `var()`
all the way down; the checks at the end fail on any hex, `rgb()` or `oklch()`
found outside the primitives.

### One declaration, two themes

Every colour that differs between the themes is declared once, in layer 2a, as
`light-dark(<light>, <dark>)`:

```css
--ts-neutral-1: light-dark(var(--prim-navy-1), var(--prim-navy-dark-1));
```

and the theme itself is nothing but the `color-scheme` property:

```css
:root               { color-scheme: light dark; } /* follow the OS */
[data-theme="light"] { color-scheme: light; }      /* the reader's choice */
[data-theme="dark"]  { color-scheme: dark; }       /* the reader's choice */
```

That is the whole mechanism. The explicit dark theme and the
`prefers-color-scheme` fallback are two ways of setting one property, not two
copies of a palette, so they cannot drift: there is nothing for either to drift
from. A token cannot exist in one theme and not the other either, because both
answers are arguments of the same declaration. Of the 286 `--ts-*` tokens,
91 are `light-dark()` declarations, 105 more vary by referring to them
(named roles such as `--ts-text-2`, and the deprecated aliases) and 90 do not
vary by theme (type, space, motion, the night band's brand colours).

Only `light-dark()` makes this possible. CSS cannot OR a selector
(`[data-theme="dark"]`) with a media query (`prefers-color-scheme: dark`) in one
rule, so any design that keeps a dark *block* needs two copies of it. The
previous `tokens.css` had exactly that — three generated blocks of 92
declarations, held equal by a script.

### Themes work on any element

`light-dark()` resolves where a colour is *used*, not where the token is
declared, so `data-theme` re-themes whatever subtree it sits on — a side-by-side
preview pane, a permanently dark band. `base.css` restates `color` and
`background-color` on `:root` and `:where([data-theme])`, because `color`
inherits as an already-resolved value.

Under the old blocks this was broken: the named roles were resolved once on
`:root`, so inside a nested dark pane **103 of 277 tokens still painted their
light value** (page, borders, text — white panels, dark-on-dark labels), and
inside a nested light pane on a dark page 195 did. Both are now **0**.

### System is the default

No `data-theme` means "follow the OS": `:root { color-scheme: light dark }`
picks the scheme from `prefers-color-scheme`, and every token follows it with
no script and no client hint. `data-theme` is only for a reader's explicit
choice. Templates should therefore write it only when the reader has chosen,
and declare `<meta name="color-scheme" content="light dark">` otherwise so
the first paint is already in the right scheme. Rendered in Chromium, WebKit
and Firefox, a page with no attribute paints all 286 tokens exactly as the
matching explicit theme does (the parity table below), so WebKit and
WKWebView, which never send `Sec-CH-Prefers-Color-Scheme`, follow the OS as
soon as the attribute is left off.

### Browsers without `light-dark()`

`light-dark()` is supported by 92.1% of global usage and `oklch()` by 95.1%
(caniuse data, 2026-10-29; Safari 17.5, Chrome and Edge 123, Firefox 120). In
the remaining ~5% a `light-dark()` token would fail where it is used and the
page would fall back to unstyled defaults. Those browsers instead match
`@supports not (color: light-dark(white, black))`, which restates the light
argument of every themed token and pins `color-scheme: light`. They get the
light theme, complete and AA-compliant, never a broken one; the theme toggle
has no visible effect there. The parity check fails if a restated value ever
differs from its light argument.

### OKLCH, and why there is an `@supports`

Each primitive is declared twice: a hex value, then the `oklch()` it was
derived from, behind `@supports (color: oklch(0% 0 0))`. OKLCH lightness is
perceptually uniform, so a lightness value means the same thing in every hue,
and a ramp is computed instead of nudged.

The usual advice — write the hex first and the `oklch()` second, and let the
browser discard what it cannot parse — **does not work for custom
properties**, which is why this file deviates from it. A custom property
accepts any token sequence when parsed, so a browser without `oklch()` stores
the second value happily and fails only later, when `var()` substitutes it into
a real property. At that point the declaration is invalid at computed-value
time and the property becomes `unset`: the hex is already gone. Measured in
Chromium with a function no browser knows, so the result does not depend on
`oklch()` support:

```text
ordinary property, hex then unknown       : rgb(11, 20, 36)  the hex survives
custom property, hex then unknown         : rgb(1, 2, 3)     inherited: the hex is gone
custom property, unknown behind @supports : rgb(11, 20, 36)  the hex survives
```

The two paths paint the same colours: all 148 primitives agree within 1/255
per channel, statically and on the 374 opaque tokens Chromium rasterised.

---

## Light and dark parity

Parity is a property of the construction, and both the construction and its
result are checked.

**Static** — `tokens.css` parsed and every `var()` and `light-dark()` resolved:

```text
theme switch: :root → light dark | [data-theme='light'] → light | [data-theme='dark'] → dark
--ts-* declared under a theme condition: 0; prefers-contrast: more overrides: 2
light-dark() tokens: 91; fallback restatements: 91; fallback == light arm: true
--ts-* names: 289; resolve in light: 289; in dark: 289
hex vs oklch() primitives: 148 pairs, max channel delta 1/255 (--prim-teal-12)
contrast pairs: 291 × 2 themes = 582 checks (328 text at 4.5:1)
lowest text pair: light 4.51 (--ts-text-3 on --ts-success-soft-hover); dark 4.52 (--ts-text-3 on --ts-surface-active)
PASS: 0 failures
```

**Runtime** — the built, minified `app.css` loaded in Chromium, WebKit and
Firefox; every `--ts-*` token painted onto a probe in each scenario, the
computed values dumped and diffed. An empty diff is the proof, and it is empty
in all three engines:

| Diff | Scenario A | Scenario B | Lines that differ |
|---|---|---|---:|
| explicit dark = OS dark | `data-theme="dark"`, OS light | no attribute, OS dark | **0** |
| explicit light = OS light | `data-theme="light"` | no attribute, OS light | **0** |
| explicit light beats OS dark | `data-theme="light"`, OS light | `data-theme="light"`, OS dark | **0** |
| nested dark pane = dark page | `data-theme="dark"` on `<html>` | dark pane inside a light page | **0** |
| nested light pane = light page | `data-theme="light"` on `<html>` | light pane inside a dark page | **0** |
| no `light-dark()` = light | `data-theme="light"` | fallback active, `data-theme="dark"`, OS dark | **0** |

The same runs confirm the preference media: under
`prefers-contrast: more` exactly two tokens change (`--ts-scrollbar-thumb`
and `--ts-scrollbar-track`), and under `prefers-reduced-motion: reduce` the
root's `scroll-behavior` is `auto` (`smooth` otherwise) while no token
changes.

Across the two themes the token *names* are identical (286 = 286, empty
diff); 196 of them differ in *value*, by design.

The guarantees that make a regression impossible rather than unlikely:

1. The only theme switch is `color-scheme`, set in exactly three rules (and
   pinned to light by the fallback).
2. No `--ts-*` token may be declared under a `data-theme` selector or a
   `prefers-color-scheme` query — that is what would need a twin.
3. A themed colour is one `light-dark()` declaration: both answers live in the
   same place, so neither can be added, removed or edited without the other.
4. The fallback block is checked to equal the light arguments, value for value.

---

## Colour ramps

Twelve steps on the Radix role model, six hues, two independently tuned scales
each. The dark scales are **not** inversions: steps 1–5 are compressed so that
near-black surfaces stay distinguishable, and 9–12 are pushed far brighter than
a flip of the light scale would put them. Naive inversion is what washes dark
midtones into undifferentiated grey.

### Neutral

`--prim-navy-*` → `--ts-neutral-*`. Tinted toward the brand navy (OKLCH hue
258, measured off `#0b1424`) rather than a pure grey. Step 1 dark *is* the brand
night `#0b1424`; step 12 light is the brand navy `#1b2a41`.

| Step | Role | Token | Light | Dark | OKLCH light | OKLCH dark |
|---:|---|---|---|---|---|---|
| 1 | app background | `--ts-neutral-1` | `#ffffff` | `#0b1424` | `oklch(100% 0 258)` | `oklch(19.2% 0.0354 261.3)` |
| 2 | subtle app background | `--ts-neutral-2` | `#f8fafc` | `#0f1c30` | `oklch(98.4% 0.003 258)` | `oklch(22.7% 0.042 258)` |
| 3 | element background | `--ts-neutral-3` | `#f3f5f8` | `#14233a` | `oklch(96.9% 0.005 258)` | `oklch(25.7% 0.047 258)` |
| 4 | element hover | `--ts-neutral-4` | `#eceff4` | `#1a2c45` | `oklch(95.1% 0.007 258)` | `oklch(29% 0.052 258)` |
| 5 | element active / selected | `--ts-neutral-5` | `#e4e8ee` | `#22334c` | `oklch(93% 0.009 258)` | `oklch(31.9% 0.05 258)` |
| 6 | subtle border | `--ts-neutral-6` | `#dbe0e7` | `#2d3d54` | `oklch(90.6% 0.011 258)` | `oklch(35.7% 0.046 258)` |
| 7 | border, focus ring | `--ts-neutral-7` | `#ccd2db` | `#3b4a61` | `oklch(86.2% 0.014 258)` | `oklch(40.7% 0.043 258)` |
| 8 | hover border | `--ts-neutral-8` | `#b4bcc8` | `#526076` | `oklch(79.3% 0.019 258)` | `oklch(48.7% 0.04 258)` |
| 9 | solid fill | `--ts-neutral-9` | `#7c8593` | `#738196` | `oklch(61.5% 0.024 258)` | `oklch(60.1% 0.036 258)` |
| 10 | solid hover | `--ts-neutral-10` | `#5d6877` | `#8d9baf` | `oklch(51.3% 0.027 258)` | `oklch(68.4% 0.034 258)` |
| 11 | low-contrast text | `--ts-neutral-11` | `#515c6c` | `#9ca9bd` | `oklch(47.2% 0.029 258)` | `oklch(73.2% 0.032 258)` |
| 12 | high-contrast text | `--ts-neutral-12` | `#1b2a41` | `#e1e8f3` | `oklch(28.4% 0.0473 258.7)` | `oklch(93% 0.016 258)` |

### Accent

`--prim-teal-*` → `--ts-accent-*`. Step 9 is the brand teal fill. On dark it
inverts to the bright teal `#5fd0d0` with night ink, because a mid teal reaches
only 3.7:1 on the night background — fine for a block of colour, not for
anything read.

| Step | Role | Token | Light | Dark | OKLCH light | OKLCH dark |
|---:|---|---|---|---|---|---|
| 1 | app background | `--ts-accent-1` | `#f5fdfd` | `#0d191a` | `oklch(98.8% 0.008 200)` | `oklch(20.4% 0.018 200)` |
| 2 | subtle app background | `--ts-accent-2` | `#eafafb` | `#0a2021` | `oklch(97.4% 0.017 200)` | `oklch(22.7% 0.028 200)` |
| 3 | element background | `--ts-accent-3` | `#d9f6f7` | `#062b2d` | `oklch(95.2% 0.03 200)` | `oklch(26.4% 0.04 200)` |
| 4 | element hover | `--ts-accent-4` | `#c8f0f2` | `#043436` | `oklch(92.8% 0.042 200)` | `oklch(29.7% 0.048 200)` |
| 5 | element active / selected | `--ts-accent-5` | `#b7e9ec` | `#013d40` | `oklch(90.1% 0.052 200)` | `oklch(32.7% 0.055 200)` |
| 6 | subtle border | `--ts-accent-6` | `#a5dfe2` | `#06484b` | `oklch(86.6% 0.06 200)` | `oklch(36.7% 0.06 200)` |
| 7 | border, focus ring | `--ts-accent-7` | `#8dd0d3` | `#11585c` | `oklch(81.4% 0.068 200)` | `oklch(42.2% 0.066 200)` |
| 8 | hover border | `--ts-accent-8` | `#64b8bc` | `#1e7478` | `oklch(73.1% 0.082 200)` | `oklch(51.2% 0.078 200)` |
| 9 | solid fill | `--ts-accent-9` | `#007479` | `#5fd0d0` | `oklch(50.9% 0.0872 200)` | `oklch(79.3% 0.1025 195.3)` |
| 10 | solid hover | `--ts-accent-10` | `#00676c` | `#7ddee3` | `oklch(46.6% 0.0827 200)` | `oklch(84.3% 0.0923 200)` |
| 11 | low-contrast text | `--ts-accent-11` | `#005d61` | `#6bc3c8` | `oklch(43.2% 0.0765 200)` | `oklch(76.4% 0.085 200)` |
| 12 | high-contrast text | `--ts-accent-12` | `#004144` | `#ade3e5` | `oklch(33.9% 0.0578 200)` | `oklch(87.8% 0.055 200)` |

### Success

`--prim-green-*` → `--ts-success-*`.

| Step | Role | Token | Light | Dark | OKLCH light | OKLCH dark |
|---:|---|---|---|---|---|---|
| 1 | app background | `--ts-success-1` | `#f7fcf7` | `#111912` | `oklch(98.6% 0.009 148)` | `oklch(20.4% 0.018 148)` |
| 2 | subtle app background | `--ts-success-2` | `#edf9ee` | `#122014` | `oklch(97.1% 0.019 148)` | `oklch(22.6% 0.03 148)` |
| 3 | element background | `--ts-success-3` | `#ddf4df` | `#142a17` | `oklch(94.5% 0.038 148)` | `oklch(26.2% 0.046 148)` |
| 4 | element hover | `--ts-success-4` | `#cdeed0` | `#15341b` | `oklch(91.8% 0.054 148)` | `oklch(29.4% 0.058 148)` |
| 5 | element active / selected | `--ts-success-5` | `#bde7c1` | `#183d1f` | `oklch(88.8% 0.068 148)` | `oklch(32.4% 0.068 148)` |
| 6 | subtle border | `--ts-success-6` | `#abddb1` | `#1e4926` | `oklch(85.1% 0.08 148)` | `oklch(36.4% 0.076 148)` |
| 7 | border, focus ring | `--ts-success-7` | `#94ce9b` | `#275a31` | `oklch(79.9% 0.094 148)` | `oklch(42% 0.086 148)` |
| 8 | hover border | `--ts-success-8` | `#70b97b` | `#347641` | `oklch(72.2% 0.116 148)` | `oklch(51% 0.108 148)` |
| 9 | solid fill | `--ts-success-9` | `#21a548` | `#4cc466` | `oklch(63.5% 0.172 148)` | `oklch(73% 0.17 148)` |
| 10 | solid hover | `--ts-success-10` | `#10943d` | `#66d37a` | `oklch(58.3% 0.164 148)` | `oklch(78% 0.16 148)` |
| 11 | low-contrast text | `--ts-success-11` | `#00772a` | `#5abf6e` | `oklch(49.6% 0.1485 148)` | `oklch(72.5% 0.15 148)` |
| 12 | high-contrast text | `--ts-success-12` | `#124a20` | `#b3e5b8` | `oklch(36% 0.09 148)` | `oklch(87.5% 0.08 148)` |

### Warning

`--prim-amber-*` → `--ts-warning-*`. Amber is the awkward hue: the vivid step 9
is *lighter* than step 8, which is correct — a vivid amber is simply bright. It
is also why amber is the one fill that never takes white ink (1.8:1).

| Step | Role | Token | Light | Dark | OKLCH light | OKLCH dark |
|---:|---|---|---|---|---|---|
| 1 | app background | `--ts-warning-1` | `#fffbf5` | `#1b160f` | `oklch(99% 0.0088 79)` | `oklch(20.4% 0.016 79)` |
| 2 | subtle app background | `--ts-warning-2` | `#fff7ea` | `#231a0c` | `oklch(97.8% 0.0194 79)` | `oklch(22.6% 0.028 79)` |
| 3 | element background | `--ts-warning-3` | `#ffeed4` | `#302208` | `oklch(95.6% 0.0391 79)` | `oklch(26.2% 0.044 79)` |
| 4 | element hover | `--ts-warning-4` | `#ffe5bb` | `#3b2805` | `oklch(93.2% 0.0612 79)` | `oklch(29.4% 0.056 79)` |
| 5 | element active / selected | `--ts-warning-5` | `#ffda9f` | `#462f01` | `oklch(90.6% 0.0858 79)` | `oklch(32.4% 0.066 79)` |
| 6 | subtle border | `--ts-warning-6` | `#fccd81` | `#533902` | `oklch(87.3% 0.108 79)` | `oklch(36.4% 0.074 79)` |
| 7 | border, focus ring | `--ts-warning-7` | `#f2c16f` | `#654706` | `oklch(83.8% 0.115 79)` | `oklch(42% 0.084 79)` |
| 8 | hover border | `--ts-warning-8` | `#e8b04d` | `#865e06` | `oklch(79.2% 0.132 79)` | `oklch(51% 0.104 79)` |
| 9 | solid fill | `--ts-warning-9` | `#fab62e` | `#f7b73d` | `oklch(81.8% 0.158 79)` | `oklch(81.8% 0.15 79)` |
| 10 | solid hover | `--ts-warning-10` | `#e7a621` | `#ffc768` | `oklch(76.8% 0.152 79)` | `oklch(86.2% 0.129 79)` |
| 11 | low-contrast text | `--ts-warning-11` | `#8b6100` | `#e8b14f` | `oklch(52.3% 0.1102 79)` | `oklch(79.2% 0.13 79)` |
| 12 | high-contrast text | `--ts-warning-12` | `#553a00` | `#f7d8a9` | `oklch(37.1% 0.0772 79)` | `oklch(89.7% 0.07 79)` |

### Danger

`--prim-red-*` → `--ts-danger-*`.

| Step | Role | Token | Light | Dark | OKLCH light | OKLCH dark |
|---:|---|---|---|---|---|---|
| 1 | app background | `--ts-danger-1` | `#fffbfa` | `#201412` | `oklch(99% 0.0048 27)` | `oklch(20.6% 0.02 27)` |
| 2 | subtle app background | `--ts-danger-2` | `#fff6f4` | `#2b1614` | `oklch(97.9% 0.0102 27)` | `oklch(23% 0.034 27)` |
| 3 | element background | `--ts-danger-3` | `#ffebe9` | `#3c1a17` | `oklch(95.6% 0.0218 27)` | `oklch(26.8% 0.054 27)` |
| 4 | element hover | `--ts-danger-4` | `#ffe0dc` | `#4c1e1a` | `oklch(93.1% 0.0349 27)` | `oklch(30.2% 0.07 27)` |
| 5 | element active / selected | `--ts-danger-5` | `#ffd3cd` | `#59231e` | `oklch(90.2% 0.0509 27)` | `oklch(33.4% 0.082 27)` |
| 6 | subtle border | `--ts-danger-6` | `#ffc2ba` | `#6b2a25` | `oklch(86.6% 0.0719 27)` | `oklch(37.7% 0.094 27)` |
| 7 | border, focus ring | `--ts-danger-7` | `#f9ada4` | `#83342e` | `oklch(81.8% 0.09 27)` | `oklch(43.4% 0.11 27)` |
| 8 | hover border | `--ts-danger-8` | `#ed8f85` | `#ad443c` | `oklch(74.7% 0.116 27)` | `oklch(52.7% 0.14 27)` |
| 9 | solid fill | `--ts-danger-9` | `#d83a35` | `#e94740` | `oklch(58.6% 0.195 27)` | `oklch(63% 0.2 27)` |
| 10 | solid hover | `--ts-danger-10` | `#c42d2a` | `#f75d53` | `oklch(53.8% 0.188 27)` | `oklch(67.8% 0.19 27)` |
| 11 | low-contrast text | `--ts-danger-11` | `#b62c29` | `#ed766b` | `oklch(51.4% 0.175 27)` | `oklch(70.1% 0.15 27)` |
| 12 | high-contrast text | `--ts-danger-12` | `#67221e` | `#fbb8af` | `oklch(35.9% 0.1 27)` | `oklch(84.1% 0.08 27)` |

### Info

`--prim-blue-*` → `--ts-info-*`. Info is blue, not a second teal: a teal
"info" badge reads as brand and spends the accent budget on something that is
not a call to action. Hue 254 is far enough from the teal to read as a
different idea and far enough from 280+ to avoid the generic indigo.

| Step | Role | Token | Light | Dark | OKLCH light | OKLCH dark |
|---:|---|---|---|---|---|---|
| 1 | app background | `--ts-info-1` | `#fafcff` | `#101821` | `oklch(99% 0.0048 254)` | `oklch(20.6% 0.022 254)` |
| 2 | subtle app background | `--ts-info-2` | `#f3f8ff` | `#101e2e` | `oklch(97.8% 0.0106 254)` | `oklch(23% 0.038 254)` |
| 3 | element background | `--ts-info-3` | `#e6f1ff` | `#102741` | `oklch(95.5% 0.0218 254)` | `oklch(26.8% 0.058 254)` |
| 4 | element hover | `--ts-info-4` | `#d9eaff` | `#0f2f53` | `oklch(93% 0.0343 254)` | `oklch(30.2% 0.076 254)` |
| 5 | element active / selected | `--ts-info-5` | `#c9e1ff` | `#0e3763` | `oklch(90.1% 0.049 254)` | `oklch(33.4% 0.09 254)` |
| 6 | subtle border | `--ts-info-6` | `#b4d6ff` | `#144275` | `oklch(86.5% 0.0677 254)` | `oklch(37.7% 0.1 254)` |
| 7 | border, focus ring | `--ts-info-7` | `#9ac6fc` | `#1c518d` | `oklch(81.5% 0.09 254)` | `oklch(43.4% 0.114 254)` |
| 8 | hover border | `--ts-info-8` | `#77aff3` | `#266bb9` | `oklch(74.2% 0.116 254)` | `oklch(52.7% 0.14 254)` |
| 9 | solid fill | `--ts-info-9` | `#1876d5` | `#1c8af9` | `oklch(56.5% 0.168 254)` | `oklch(63.5% 0.19 254)` |
| 10 | solid hover | `--ts-info-10` | `#0866c0` | `#449bff` | `oklch(51.5% 0.16 254)` | `oklch(68.5% 0.1694 254)` |
| 11 | low-contrast text | `--ts-info-11` | `#095eb1` | `#5ea2f5` | `oklch(48.6% 0.15 254)` | `oklch(70.5% 0.14 254)` |
| 12 | high-contrast text | `--ts-info-12` | `#0f3763` | `#b1d4fe` | `oklch(33.5% 0.09 254)` | `oklch(85.8% 0.07 254)` |

### Retuned steps

A full text-on-surface matrix (below) found 14 pairings under their minimum —
12 of them text under 4.5:1 — that the previous 80-pair table never checked:
tertiary text and teal links on selected rows, inks on hovered tints. Nine steps moved by 0.7–2.2 OKLCH lightness points, the
smallest change that clears every pair; hue and chroma are untouched:

| Primitive | Before | After | Why |
|---|---|---|---|
| `--prim-navy-9` | `#7f8997` 62.5% | `#7c8593` 61.5% | disabled text and input edges on a selected row reach 3:1 |
| `--prim-navy-10` | `#616c7b` 52.6% | `#5d6877` 51.3% | tertiary text on selected rows and tinted hovers reaches 4.5:1 |
| `--prim-navy-11` | `#555f6f` 48.4% | `#515c6c` 47.2% | keeps secondary visibly apart from the darker tertiary |
| `--prim-teal-9` | `#0f7b80` 53.1% | `#007479` 50.9% | teal link text on hovered and selected rows reaches 4.5:1; white on the fill goes 5.04 → 5.56 |
| `--prim-teal-10` | `#006d72` 48.6% | `#00676c` 46.6% | keeps the hover step distinct from the new step 9 |
| `--prim-teal-11` | `#006166` 44.9% | `#005d61` 43.2% | keeps the pressed step distinct |
| `--prim-green-11` | `#007c2f` 51.3% | `#00772a` 49.6% | success ink on a hovered success tint reaches 4.5:1 |
| `--prim-amber-11` | `#8d6300` 53.0% | `#8b6100` 52.3% | amber ink on a hovered amber tint reaches 4.5:1 |
| `--prim-navy-dark-10` | `#8b98ad` 67.7% | `#8d9baf` 68.4% | dark tertiary text on a selected row reaches 4.5:1 |

One role moved too: `--ts-series-hover` was teal step 8 in light, which drops a
hovered bar to 2.3:1 against the page. It is now step 10 in both themes
(6.36:1 / 9.01:1 at worst), darkening on hover in light like the primary does.

The favicon and `site.webmanifest` still draw the teal as `#0f7b80`; a logo
mark owes 3:1, not 4.5:1, so they need not follow, but they may.

### Standalone data hues

Chart series 4 and 6 need two more distinguishable hues and never a ramp — they
are only ever a stroke or a fill: `--prim-violet-9` `#7a4fd0` / dark `#a98ce6`,
`--prim-clay-9` `#b4542f` / dark `#e0906f`, exposed as `--ts-data-4` and
`--ts-data-6`. The only violet in the system is chart series 4.

---

## The role model

Every semantic colour is a ramp step chosen by **role**, and it is the same
step in both themes; the ramp underneath has already resolved the theme. The
twelve roles are Radix's, applied to every hue:

| Step | Role | Neutral roles | Accent and status roles |
|---:|---|---|---|
| 1 | app background | `--ts-background` | — |
| 2 | subtle app background | `--ts-surface-1` | — |
| 3 | element background | `--ts-surface-2` | `--ts-primary-soft`, `--ts-<status>-soft`, `--ts-series-track` |
| 4 | hover | `--ts-surface-hover` | `--ts-primary-soft-hover`, `--ts-<status>-soft-hover` |
| 5 | active, selected | `--ts-surface-active` | — |
| 6 | subtle border | `--ts-border-subtle`, `--ts-chart-grid` | — |
| 7 | border | `--ts-border` | `--ts-primary-border`, `--ts-<status>-border` |
| 8 | hover border | `--ts-border-hover` (`--ts-border-strong` is its alias) | — |
| 9 | solid | `--ts-idle` | `--ts-primary`, `--ts-<status>`, `--ts-series-1` |
| 10 | solid hover | `--ts-input-hover` | `--ts-primary-hover`, `--ts-series-hover` |
| 11 | low-contrast text | `--ts-text-2`, `--ts-byoc-soft-foreground` | `--ts-primary-soft-foreground`, `--ts-<status>-ink` |
| 12 | high-contrast text | `--ts-text-1`, `--ts-text-heading`, `--ts-surface-inverse` | — |

Where WCAG asks for more than a role gives, the role yields, and the token
says why where it is declared:

| Token | Role step | Takes | Why |
|---|---:|---:|---|
| `--ts-ring` | 7 | accent 9 | a focus indicator owes 3:1 against what it surrounds; accent 7 is 1.7:1 on white |
| `--ts-input`, `--ts-scrollbar-thumb` | 7 | 9 | a control's boundary owes 3:1 (SC 1.4.11); step 7 is 1.5:1 in light |
| `--ts-text-3`, `--ts-chart-axis` | — | 10 | the role model has two text steps and the product needs three; step 10 is tuned for text, 4.51:1 at worst |
| `--ts-text-disabled` | — | 9 | exempt from contrast, held to 3:1 anyway |
| `--ts-primary-active` | — | 11 | pressed is one step past hover; the ramp's next step is 11 |
| `--ts-byoc`, `--ts-byoc-strong` | 9 | 11, 12 | BYOC is the quieter of the two deployment fills, so its solids are the neutral ink steps |
| `--ts-success-solid`, `--ts-danger-solid` (light) | 9 | 11, 10 | a filled badge carries white ink at 4.5:1 |
| `--ts-surface-raised`, `--ts-surface-overlay` (dark) | 1 | 4 | in dark, lightness is what reads as height (see Elevation) |

### What the rebind moved

The lines sat one step lighter than their roles, which put the subtle
border on step 5, the selected-row step: a table rule crossing a selected
row was the same colour as the row, 1.00:1, and vanished. Each line now
sits on its role step:

| Token | Was | Is | Light | Dark | Against the page, light / dark |
|---|---:|---:|---|---|---|
| `--ts-border-subtle` | 5 | 6 | `#e4e8ee` → `#dbe0e7` | `#22334c` → `#2d3d54` | 1.33 / 1.67; 1.08 / 1.16 on a selected row |
| `--ts-border` | 6 | 7 | `#dbe0e7` → `#ccd2db` | `#2d3d54` → `#3b4a61` | 1.33 → 1.52 / 1.67 → 2.05 |
| `--ts-border-strong` | 7 | 8 | `#ccd2db` → `#b4bcc8` | `#3b4a61` → `#526076` | now the alias of `--ts-border-hover` |
| `--ts-chart-grid` | 5 | 6 | `#e4e8ee` → `#dbe0e7` | `#22334c` → `#2d3d54` | 1.33 / 1.67 |

New roles: `--ts-input-hover` (10), the edge of a control under the pointer,
which now darkens in light and brightens in dark where it used to fade from
step 9 to step 7; `--ts-surface-inverse` (12), the tooltip's fill, which
used to borrow a deprecated ramp name.

The components moved with the tokens, so a control looks as it did and
reacts by role: a control at rest takes `--ts-border` (the step 7 that
`--ts-border-strong` used to be), hover takes `--ts-surface-hover` or
`--ts-border-hover`, a press takes `--ts-surface-active`, and the switch's
edge takes `--ts-input`, so an off switch now has the 3:1 boundary every
other control has. Edges in dark gain the most: there a border is all a
flat surface has (Elevation), and the default edge goes from 1.67:1 to
2.05:1 against the night.

---

## Semantic reference

Every `--ts-*` token, its value in both themes (hex is what is declared first;
the OKLCH twin paints within 1/255), what it is for and its measured contrast.
For a foreground the contrast column is its worst pairing in the matrix below;
for a surface it is the lowest text ratio measured on it.

### Surfaces

1–2 are page, 3–5 are things on the page. A panel never sits on a surface more
than one step from its own.

| Token | Light | Dark | Purpose | Contrast light / dark |
|---|---|---|---|---|
| `--ts-background` | `#ffffff` | `#0b1424` | the page | text on it ≥ 5.52 |
| `--ts-surface-1` | `#f8fafc` | `#0f1c30` | panels, cards, striped rows | text on it ≥ 5.27 |
| `--ts-surface-2` | `#f3f5f8` | `#14233a` | a panel inside a panel, table headers, pagination | text on it ≥ 5.05 |
| `--ts-surface-3` | `#eceff4` | `#1a2c45` | the deepest nesting step; neutral chips | text on it ≥ 4.83 |
| `--ts-surface-hover` | `#eceff4` | `#1a2c45` | hovered rows and list items | text on it ≥ 4.83 |
| `--ts-surface-active` | `#e4e8ee` | `#22334c` | selected rows and items | text on it ≥ 4.52 |
| `--ts-surface-raised` | `#ffffff` | `#1a2c45` | anything that lifts: cards, dialogs, toasts, fields. White in light, a *lighter* step in dark | text on it ≥ 4.97 |
| `--ts-surface-overlay` | `#ffffff` | `#1a2c45` | popovers, menus, dropdowns | text on it ≥ 4.99 |
| `--ts-surface-inset` | `#f3f5f8` | `#0b1424` | code blocks, wells, read-only fields. Steps up in light, down to the page in dark | text on it ≥ 5.09 |
| `--ts-surface-inverse` | `#1b2a41` | `#e1e8f3` | an inverted surface (the tooltip): body ink as a fill | text on it ≥ 14.44 |
| `--ts-overlay` | `rgb(11 20 36 / 0.45)` | `rgb(3 8 16 / 0.72)` | the scrim behind a modal (alias of `--ts-scrim`) | translucent veil |

### Text

Four tones. Headings and body deliberately share step 12 — a two-point contrast
difference nobody can see is not hierarchy; weight and size are.

| Token | Light | Dark | Purpose | Contrast light / dark |
|---|---|---|---|---|
| `--ts-text-1` | `#1b2a41` | `#e1e8f3` | body copy | 11.51 / 10.35 (min 4.5) |
| `--ts-text-heading` | `#1b2a41` | `#e1e8f3` | headings; same ink as body, set apart by weight and size | 11.51 / 10.35 (min 4.5) |
| `--ts-text-2` | `#515c6c` | `#9ca9bd` | secondary text, labels, table headers | 5.40 / 5.36 (min 4.5) |
| `--ts-text-3` | `#5d6877` | `#8d9baf` | tertiary text: captions, hints, metadata | 4.51 / 4.52 (min 4.5) |
| `--ts-text-disabled` | `#7c8593` | `#738196` | disabled labels and placeholders (WCAG-exempt, still held to 3:1) | 3.03 / 3.23 (min 3) |
| `--ts-text-inverse` | `#ffffff` | `#0b1424` | ink on `--ts-surface-inverse` (the tooltip) | 14.44 / 14.95 (min 4.5) |
| `--ts-text-on-solid` | `#ffffff` | `#0b1424` | ink for every solid fill except amber | 4.57 / 4.77 (min 4.5) |
| `--ts-text-on-primary` | `#ffffff` | `#0b1424` | alias of `--ts-primary-foreground` | 5.56 / 9.01 (min 4.5) |
| `--ts-text-on-warning` | `#553a00` | `#1b160f` | ink for amber fills; amber never takes white | 5.90 / 10.10 (min 4.5) |

### Lines

A decorative line only has to be visible; a control boundary owes 3:1
against whatever it sits on, which is why inputs take step 9 and not step 7.

| Token | Light | Dark | Purpose | Contrast light / dark |
|---|---|---|---|---|
| `--ts-border-subtle` | `#dbe0e7` | `#2d3d54` | internal dividers, table rules (step 6) | decorative; 1.08 / 1.16 on a selected row |
| `--ts-border` | `#ccd2db` | `#3b4a61` | the edge of a panel, card, field or resting control (step 7) | decorative; 1.52 / 2.05 on the page |
| `--ts-border-hover` | `#b4bcc8` | `#526076` | a bordered control under the pointer (step 8) | decorative |
| `--ts-border-strong` | `#b4bcc8` | `#526076` | alias of `--ts-border-hover`: an edge that has to stand out | decorative |
| `--ts-input` | `#7c8593` | `#738196` | control boundaries that must clear 3:1: inputs, checkboxes, switches | 3.03 / 3.23 (min 3) |
| `--ts-input-hover` | `#5d6877` | `#8d9baf` | a control boundary under the pointer: darker in light, brighter in dark | 4.60 / 4.52 (min 3) |
| `--ts-ring` | `#007479` | `#5fd0d0` | the focus ring | 4.52 / 6.94 (min 3) |

#### Line widths

Two widths, the same in both themes. The design linter rejects a raw border,
outline or column-rule width anywhere but `tokens.css`, hairlines included
(`docs/checks/design-lint.md`), so a component writes
`border: var(--ts-border-width) solid var(--ts-border)`, never `1px`.

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-border-width` | `1px` | the hairline: every border, divider, table rule and control outline |
| `--ts-border-width-strong` | `2px` | emphasis only: the active tab or contents marker, a rule beside a focus ring |

Use the hairline for every edge, divider and table rule, and for the outline
of a control. Reach for the strong width only to mark something: the active
tab's underline, the current entry in a table of contents, a rule that sits
beside a focus ring. Thickness is emphasis, so a card, a field or a panel at
2px is shouting; a raised surface gets its separation from elevation instead.
The focus ring is an outline, not a border, and has tokens of its own.

#### Focus ring

What ships today, counted across `web/assets/css/*.css` and
`web/components/components.css` in the converged tree: the ring is 2px in
every rule that draws one (20 of 20), and it sits 2px outside the element in 13
of 23 `outline-offset` rules. The tokens take those values, so adopting them
changes nothing on screen. No rule uses a 3px offset.

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-focus-ring-width` | `2px` | the focus outline every control draws |
| `--ts-focus-ring-offset` | `2px` | the gap between the ring and the element; negate it for an inset ring |

```css
:focus-visible {
  outline: var(--ts-focus-ring-width) solid var(--ts-ring);
  outline-offset: var(--ts-focus-ring-offset);
}
```

The other offsets in use are deliberate, and each is a calculation on the
token rather than a new one, which the design linter accepts:

| Today | Where | Write it as |
|---|---|---|
| `-2px` | the scrolling code block and tab strip, and scroll panes, where an outside ring would be clipped | `calc(var(--ts-focus-ring-offset) * -1)` |
| `1px` | the small dismiss buttons inside badges, alerts and toasts | `calc(var(--ts-focus-ring-offset) / 2)` |
| `-1px` | inputs, textareas and selects: the ring drawn over the field's own border | `calc(var(--ts-focus-ring-offset) / -2)` |
| `4px` | one settings ring (`.st-vault`), via `--ts-space-1` | `calc(var(--ts-focus-ring-offset) * 2)` |

#### Scrollbars

The thumb is a control, so it owes 3:1 against whatever it scrolls over — the
track is transparent, so that means every surface, selected rows included. It
used to borrow `--ts-border-strong`, a decorative step: 1.52:1 in light and
2.05:1 in dark. It now takes step 9, the same as a control edge.

| Token | Light | Dark | Purpose | Contrast light / dark |
|---|---|---|---|---|
| `--ts-scrollbar-thumb` | `#7c8593` | `#738196` | the scrollbar thumb, on a transparent track; body ink under `prefers-contrast: more` | 3.03 / 3.23 (min 3) |
| `--ts-scrollbar-track` | `transparent` | `transparent` | transparent; the page colour under `prefers-contrast: more` | transparent: the thumb is measured against the surface behind it |

`scrollbar-color` inherits, so `base.css` sets it once, where the theme is
set (`:root` and `:where([data-theme])`, so a re-themed pane gets its own
thumb), instead of on every element. `scrollbar-width` does not inherit:
the page's own scrollbar is thin, and an inner scroller keeps the platform's
width unless it opts in with `scrollbar-width: thin` beside its `overflow`.
macOS shows custom colours only on scrollers that set a width, so an opted-out
inner scroller there gets the native overlay bar, which is accessible as it
is. The `::-webkit-scrollbar` fallback was dropped: every browser that would
use it has native scrollbars that already meet the bar.

Under `prefers-contrast: more` the two tokens swap to body ink on an opaque
page track, 14.44:1 / 14.95:1. Forced-colours mode ignores `scrollbar-color`
and uses the system's own colours.

### Primary — the rationed teal

| Token | Light | Dark | Purpose | Contrast light / dark |
|---|---|---|---|---|
| `--ts-primary` | `#007479` | `#5fd0d0` | the one call to action on the screen; link text | 4.52 / 6.94 (min 4.5) |
| `--ts-primary-hover` | `#00676c` | `#7ddee3` | its hover | 5.41 / 8.16 (min 4.5) |
| `--ts-primary-active` | `#005d61` | `#6bc3c8` | its pressed state | text on it ≥ 7.67 |
| `--ts-primary-foreground` | `#ffffff` | `#0b1424` | ink on the teal fill | 5.56 / 9.01 (min 4.5) |
| `--ts-primary-soft` | `#d9f6f7` | `#062b2d` | tinted chip, selected nav item | text on it ≥ 4.98 |
| `--ts-primary-soft-hover` | `#c8f0f2` | `#043436` | its hover | text on it ≥ 4.63 |
| `--ts-primary-soft-foreground` | `#005d61` | `#6bc3c8` | teal ink on the tint | 6.28 / 6.62 (min 4.5) |
| `--ts-primary-border` | `#8dd0d3` | `#11585c` | the edge of a tinted chip | decorative: no minimum |

### BYOC

Navy marks "runs inside your cloud"; teal marks "runs in Timlee's". The pair
separates by hue *and* by lightness, so the distinction survives greyscale and
colour-vision deficiency. BYOC draws from the neutral ramp rather than a
seventh hue — it is deliberately the quieter of the two.

| Token | Light | Dark | Purpose | Contrast light / dark |
|---|---|---|---|---|
| `--ts-byoc` | `#515c6c` | `#9ca9bd` | BYOC fill ("runs in your cloud") | text on it ≥ 6.78 |
| `--ts-byoc-strong` | `#1b2a41` | `#e1e8f3` | emphasised BYOC fill | text on it ≥ 14.44 |
| `--ts-byoc-foreground` | `#ffffff` | `#0b1424` | ink on a BYOC fill | 6.78 / 7.74 (min 4.5) |
| `--ts-byoc-soft` | `#f3f5f8` | `#14233a` | BYOC chip | text on it ≥ 5.18 |
| `--ts-byoc-soft-foreground` | `#515c6c` | `#9ca9bd` | ink on the BYOC chip | 6.21 / 5.92 (min 4.5) |

### Status

`--ts-<role>` fills a shape, and its contrast is measured against the ink on
it. `--ts-<role>-dot` stands alone on a surface and is measured against that
surface. **They are different tokens because they cannot be the same colour:**
`--ts-warning` is 1.78:1 on white, and any amber that does reach the 3:1 a
standalone indicator owes has stopped being amber and become brown.

| Token | Light | Dark | Purpose | Contrast light / dark |
|---|---|---|---|---|
| `--ts-success` | `#21a548` | `#4cc466` | vivid success fill (shapes, progress) | shapes only; never carries text |
| `--ts-success-solid` | `#00772a` | `#4cc466` | filled success badge: tuned so white ink clears 4.5:1 | text on it ≥ 5.72 |
| `--ts-success-foreground` | `#ffffff` | `#0b1424` | ink on a success fill | 5.72 / 8.24 (min 4.5) |
| `--ts-success-soft` | `#ddf4df` | `#142a17` | success chip and callout background | text on it ≥ 4.87 |
| `--ts-success-soft-hover` | `#cdeed0` | `#15341b` | its hover | text on it ≥ 4.51 |
| `--ts-success-ink` | `#00772a` | `#5abf6e` | success text | 4.56 / 5.92 (min 4.5) |
| `--ts-success-border` | `#94ce9b` | `#275a31` | success chip and callout edge | decorative: no minimum |
| `--ts-success-dot` | `#00772a` | `#4cc466` | standalone success dot, measured against the surface it sits on | 5.24 / 6.30 (min 3) |
| `--ts-warning` | `#fab62e` | `#f7b73d` | vivid amber fill; takes dark ink only | text on it ≥ 5.90 |
| `--ts-warning-solid` | `#fab62e` | `#f7b73d` | filled amber badge | text on it ≥ 5.90 |
| `--ts-warning-foreground` | `#553a00` | `#1b160f` | ink on a warning fill | 5.90 / 10.10 (min 4.5) |
| `--ts-warning-soft` | `#ffeed4` | `#302208` | warning chip and callout background | text on it ≥ 4.84 |
| `--ts-warning-soft-hover` | `#ffe5bb` | `#3b2805` | its hover | text on it ≥ 4.51 |
| `--ts-warning-ink` | `#8b6100` | `#e8b14f` | warning text | 4.51 / 7.26 (min 4.5) |
| `--ts-warning-border` | `#f2c16f` | `#654706` | warning chip and callout edge | decorative: no minimum |
| `--ts-warning-dot` | `#8b6100` | `#e8b14f` | standalone warning dot, measured against the surface it sits on | 5.05 / 7.27 (min 3) |
| `--ts-danger` | `#d83a35` | `#e94740` | vivid danger fill (shapes, progress) | text on it ≥ 4.59 |
| `--ts-danger-solid` | `#c42d2a` | `#e94740` | filled danger badge | text on it ≥ 4.77 |
| `--ts-danger-foreground` | `#ffffff` | `#0b1424` | ink on a danger fill | 4.59 / 4.77 (min 4.5) |
| `--ts-danger-soft` | `#ffebe9` | `#3c1a17` | danger chip and callout background | text on it ≥ 4.93 |
| `--ts-danger-soft-hover` | `#ffe0dc` | `#4c1e1a` | its hover | text on it ≥ 4.57 |
| `--ts-danger-ink` | `#b62c29` | `#ed766b` | danger text | 4.99 / 4.91 (min 4.5) |
| `--ts-danger-border` | `#f9ada4` | `#83342e` | danger chip and callout edge | decorative: no minimum |
| `--ts-danger-dot` | `#c42d2a` | `#e94740` | standalone danger dot, measured against the surface it sits on | 5.12 / 3.65 (min 3) |
| `--ts-info` | `#1876d5` | `#1c8af9` | vivid info fill (shapes, progress) | text on it ≥ 4.57 |
| `--ts-info-solid` | `#1876d5` | `#1c8af9` | filled info badge | text on it ≥ 4.57 |
| `--ts-info-foreground` | `#ffffff` | `#0b1424` | ink on a info fill | 4.57 / 5.31 (min 4.5) |
| `--ts-info-soft` | `#e6f1ff` | `#102741` | info chip and callout background | text on it ≥ 4.95 |
| `--ts-info-soft-hover` | `#d9eaff` | `#0f2f53` | its hover | text on it ≥ 4.62 |
| `--ts-info-ink` | `#095eb1` | `#5ea2f5` | info text | 5.29 / 5.13 (min 4.5) |
| `--ts-info-border` | `#9ac6fc` | `#1c518d` | info chip and callout edge | decorative: no minimum |
| `--ts-info-dot` | `#1876d5` | `#1c8af9` | standalone info dot, measured against the surface it sits on | 4.19 / 4.06 (min 3) |
| `--ts-idle` | `#7c8593` | `#738196` | idle / unknown fill | 3.41 / 3.56 (min 3) |
| `--ts-idle-dot` | `#7c8593` | `#738196` | standalone idle dot | 3.41 / 3.56 (min 3) |

### Data visualisation

| Token | Light | Dark | Purpose | Contrast light / dark |
|---|---|---|---|---|
| `--ts-series-1` | `#007479` | `#5fd0d0` | primary metric (the brand teal: the live state the accent is for) | 5.32 / 7.66 (min 3) |
| `--ts-series-2` | `#515c6c` | `#9ca9bd` | second series | 6.48 / 5.92 (min 3) |
| `--ts-series-3` | `#8b6100` | `#f7b73d` | third series; step 11 in light, because a 1.8:1 amber line is not a line | 5.27 / 7.92 (min 3) |
| `--ts-series-4` | `#7a4fd0` | `#a98ce6` | fourth series | 5.20 / 5.09 (min 3) |
| `--ts-series-5` | `#00772a` | `#4cc466` | fifth series | 5.47 / 6.30 (min 3) |
| `--ts-series-6` | `#b4542f` | `#e0906f` | sixth series | 4.72 / 5.63 (min 3) |
| `--ts-series-hover` | `#00676c` | `#7ddee3` | hovered series; brightens in dark, darkens in light | 6.36 / 9.01 (min 3) |
| `--ts-series-track` | `#d9f6f7` | `#062b2d` | the unfilled part of a bar or gauge | decorative: no minimum |
| `--ts-chart-grid` | `#dbe0e7` | `#2d3d54` | grid lines (step 6, a subtle border) | decorative: no minimum |
| `--ts-chart-axis` | `#5d6877` | `#8d9baf` | axis labels | 5.18 / 4.99 (min 4.5) |
| `--ts-data-4` | `#7a4fd0` | `#a98ce6` | chart hue for series 4 (no ramp) | 5.20 / 5.09 (min 3) |
| `--ts-data-6` | `#b4542f` | `#e0906f` | chart hue for series 6 (no ramp) | 4.72 / 5.63 (min 3) |

### Washes and scrims

Translucent, so they have no contrast of their own: they darken with ink in
light and lighten in dark, because darkening near-black does nothing.

| Token | Light | Dark | Purpose |
|---|---|---|---|
| `--ts-scrim` | `rgb(11 20 36 / 0.45)` | `rgb(3 8 16 / 0.72)` | full-page veil; near-opaque in dark, where 45% reads as nothing |
| `--ts-wash-subtle` | `rgb(15 30 50 / 0.05)` | `rgb(255 255 255 / 0.04)` | barely-there translucent hover: ink in light, light in dark |
| `--ts-wash` | `rgb(15 30 50 / 0.08)` | `rgb(255 255 255 / 0.07)` | standard translucent hover |
| `--ts-wash-strong` | `rgb(15 30 50 / 0.12)` | `rgb(255 255 255 / 0.1)` | pressed translucent state |

### Theme-invariant brand colours

The only `--ts-*` colours that deliberately do not follow the theme: the night
band on the marketing page is night in both.

| Token | Light | Dark | Purpose | Contrast light / dark |
|---|---|---|---|---|
| `--ts-brand-teal` | `#007479` | `#007479` | brand teal on a permanently dark band or in artwork | artwork and night-band edges |
| `--ts-brand-teal-dark` | `#00676c` | `#00676c` | its darker step | artwork and night-band edges |
| `--ts-brand-teal-glow` | `#5fd0d0` | `#5fd0d0` | the bright teal used on night bands | artwork and night-band edges |
| `--ts-brand-navy` | `#1b2a41` | `#1b2a41` | the brand navy | artwork and night-band edges |
| `--ts-brand-night` | `#0b1424` | `#0b1424` | the night band background | text on it ≥ 18.42 |
| `--ts-brand-slate` | `#515c6c` | `#515c6c` | muted navy for artwork | artwork and night-band edges |
| `--ts-on-dark-text` | `#ffffff` | `#ffffff` | ink on a night band | 18.42 / 18.42 (min 4.5) |
| `--ts-on-dark-border` | `rgb(255 255 255 / 0.1)` | `rgb(255 255 255 / 0.1)` | hairline on a night band | decorative: no minimum |
| `--ts-on-dark-border-hover` | `rgb(255 255 255 / 0.45)` | `rgb(255 255 255 / 0.45)` | its hover | decorative: no minimum |
| `--ts-on-dark-wash` | `rgb(255 255 255 / 0.1)` | `rgb(255 255 255 / 0.1)` | hover fill on a night band | artwork and night-band edges |

---

## Measured contrast

WCAG 2.x relative luminance, computed from the shipped `tokens.css` with every
`var()` and `light-dark()` resolved, for both the hex and the OKLCH path; each
cell is the worse of the two. **291 pairings × 2 themes = 582 checks
(328 of them text), 0 failures.** Text owes 4.5:1. Disabled text is exempt under WCAG and is held to
3:1 anyway. Non-text UI — focus rings, control edges, status dots, chart
series — owes 3:1.

"Every surface" means all nine neutral surfaces, including hovered and selected
rows, and all eleven tints with their hover states: a caption in a selected
row or a link in a hovered row is a real pairing, and it is exactly where the
old table had blind spots.

| Foreground | Background | Min | Light | Dark |
|---|---|---:|---:|---:|
| **Body text on every surface** | | | | |
| `--ts-text-1` | `--ts-background` | 4.5 | 14.44 | 14.95 |
| `--ts-text-1` | `--ts-surface-1` | 4.5 | 13.80 | 13.86 |
| `--ts-text-1` | `--ts-surface-2` | 4.5 | 13.22 | 12.68 |
| `--ts-text-1` | `--ts-surface-3` | 4.5 | 12.53 | 11.43 |
| `--ts-text-1` | `--ts-surface-hover` | 4.5 | 12.53 | 11.43 |
| `--ts-text-1` | `--ts-surface-active` | 4.5 | 11.74 | 10.35 |
| `--ts-text-1` | `--ts-surface-raised` | 4.5 | 14.44 | 11.43 |
| `--ts-text-1` | `--ts-surface-overlay` | 4.5 | 14.44 | 11.43 |
| `--ts-text-1` | `--ts-surface-inset` | 4.5 | 13.22 | 14.95 |
| `--ts-text-1` | `--ts-primary-soft` | 4.5 | 12.71 | 12.25 |
| `--ts-text-1` | `--ts-primary-soft-hover` | 4.5 | 11.83 | 11.00 |
| `--ts-text-1` | `--ts-success-soft` | 4.5 | 12.43 | 12.28 |
| `--ts-text-1` | `--ts-success-soft-hover` | 4.5 | 11.51 | 11.07 |
| `--ts-text-1` | `--ts-warning-soft` | 4.5 | 12.68 | 12.54 |
| `--ts-text-1` | `--ts-warning-soft-hover` | 4.5 | 11.81 | 11.42 |
| `--ts-text-1` | `--ts-danger-soft` | 4.5 | 12.59 | 12.60 |
| `--ts-text-1` | `--ts-danger-soft-hover` | 4.5 | 11.65 | 11.30 |
| `--ts-text-1` | `--ts-info-soft` | 4.5 | 12.65 | 12.27 |
| `--ts-text-1` | `--ts-info-soft-hover` | 4.5 | 11.80 | 10.98 |
| `--ts-text-1` | `--ts-byoc-soft` | 4.5 | 13.22 | 12.68 |
| `--ts-text-heading` | `--ts-background` | 4.5 | 14.44 | 14.95 |
| `--ts-text-heading` | `--ts-surface-1` | 4.5 | 13.80 | 13.86 |
| `--ts-text-heading` | `--ts-surface-2` | 4.5 | 13.22 | 12.68 |
| `--ts-text-heading` | `--ts-surface-3` | 4.5 | 12.53 | 11.43 |
| `--ts-text-heading` | `--ts-surface-hover` | 4.5 | 12.53 | 11.43 |
| `--ts-text-heading` | `--ts-surface-active` | 4.5 | 11.74 | 10.35 |
| `--ts-text-heading` | `--ts-surface-raised` | 4.5 | 14.44 | 11.43 |
| `--ts-text-heading` | `--ts-surface-overlay` | 4.5 | 14.44 | 11.43 |
| `--ts-text-heading` | `--ts-surface-inset` | 4.5 | 13.22 | 14.95 |
| `--ts-text-heading` | `--ts-primary-soft` | 4.5 | 12.71 | 12.25 |
| `--ts-text-heading` | `--ts-primary-soft-hover` | 4.5 | 11.83 | 11.00 |
| `--ts-text-heading` | `--ts-success-soft` | 4.5 | 12.43 | 12.28 |
| `--ts-text-heading` | `--ts-success-soft-hover` | 4.5 | 11.51 | 11.07 |
| `--ts-text-heading` | `--ts-warning-soft` | 4.5 | 12.68 | 12.54 |
| `--ts-text-heading` | `--ts-warning-soft-hover` | 4.5 | 11.81 | 11.42 |
| `--ts-text-heading` | `--ts-danger-soft` | 4.5 | 12.59 | 12.60 |
| `--ts-text-heading` | `--ts-danger-soft-hover` | 4.5 | 11.65 | 11.30 |
| `--ts-text-heading` | `--ts-info-soft` | 4.5 | 12.65 | 12.27 |
| `--ts-text-heading` | `--ts-info-soft-hover` | 4.5 | 11.80 | 10.98 |
| `--ts-text-heading` | `--ts-byoc-soft` | 4.5 | 13.22 | 12.68 |
| `--ts-text-2` | `--ts-background` | 4.5 | 6.78 | 7.74 |
| `--ts-text-2` | `--ts-surface-1` | 4.5 | 6.48 | 7.18 |
| `--ts-text-2` | `--ts-surface-2` | 4.5 | 6.21 | 6.57 |
| `--ts-text-2` | `--ts-surface-3` | 4.5 | 5.88 | 5.92 |
| `--ts-text-2` | `--ts-surface-hover` | 4.5 | 5.88 | 5.92 |
| `--ts-text-2` | `--ts-surface-active` | 4.5 | 5.51 | 5.36 |
| `--ts-text-2` | `--ts-surface-raised` | 4.5 | 6.78 | 5.92 |
| `--ts-text-2` | `--ts-surface-overlay` | 4.5 | 6.78 | 5.92 |
| `--ts-text-2` | `--ts-surface-inset` | 4.5 | 6.21 | 7.74 |
| `--ts-text-2` | `--ts-primary-soft` | 4.5 | 5.97 | 6.34 |
| `--ts-text-2` | `--ts-primary-soft-hover` | 4.5 | 5.55 | 5.69 |
| `--ts-text-2` | `--ts-success-soft` | 4.5 | 5.84 | 6.36 |
| `--ts-text-2` | `--ts-success-soft-hover` | 4.5 | 5.40 | 5.73 |
| `--ts-text-2` | `--ts-warning-soft` | 4.5 | 5.95 | 6.50 |
| `--ts-text-2` | `--ts-warning-soft-hover` | 4.5 | 5.55 | 5.92 |
| `--ts-text-2` | `--ts-danger-soft` | 4.5 | 5.91 | 6.53 |
| `--ts-text-2` | `--ts-danger-soft-hover` | 4.5 | 5.47 | 5.85 |
| `--ts-text-2` | `--ts-info-soft` | 4.5 | 5.94 | 6.35 |
| `--ts-text-2` | `--ts-info-soft-hover` | 4.5 | 5.54 | 5.69 |
| `--ts-text-2` | `--ts-byoc-soft` | 4.5 | 6.21 | 6.57 |
| `--ts-text-3` | `--ts-background` | 4.5 | 5.66 | 6.53 |
| `--ts-text-3` | `--ts-surface-1` | 4.5 | 5.41 | 6.05 |
| `--ts-text-3` | `--ts-surface-2` | 4.5 | 5.18 | 5.54 |
| `--ts-text-3` | `--ts-surface-3` | 4.5 | 4.91 | 4.99 |
| `--ts-text-3` | `--ts-surface-hover` | 4.5 | 4.91 | 4.99 |
| `--ts-text-3` | `--ts-surface-active` | 4.5 | 4.60 | 4.52 |
| `--ts-text-3` | `--ts-surface-raised` | 4.5 | 5.66 | 4.99 |
| `--ts-text-3` | `--ts-surface-overlay` | 4.5 | 5.66 | 4.99 |
| `--ts-text-3` | `--ts-surface-inset` | 4.5 | 5.18 | 6.53 |
| `--ts-text-3` | `--ts-primary-soft` | 4.5 | 4.98 | 5.35 |
| `--ts-text-3` | `--ts-primary-soft-hover` | 4.5 | 4.63 | 4.80 |
| `--ts-text-3` | `--ts-success-soft` | 4.5 | 4.87 | 5.36 |
| `--ts-text-3` | `--ts-success-soft-hover` | 4.5 | 4.51 | 4.83 |
| `--ts-text-3` | `--ts-warning-soft` | 4.5 | 4.97 | 5.48 |
| `--ts-text-3` | `--ts-warning-soft-hover` | 4.5 | 4.63 | 4.99 |
| `--ts-text-3` | `--ts-danger-soft` | 4.5 | 4.93 | 5.50 |
| `--ts-text-3` | `--ts-danger-soft-hover` | 4.5 | 4.57 | 4.93 |
| `--ts-text-3` | `--ts-info-soft` | 4.5 | 4.95 | 5.36 |
| `--ts-text-3` | `--ts-info-soft-hover` | 4.5 | 4.62 | 4.79 |
| `--ts-text-3` | `--ts-byoc-soft` | 4.5 | 5.18 | 5.54 |
| **Accent text (links, link buttons)** | | | | |
| `--ts-primary` | `--ts-background` | 4.5 | 5.56 | 10.02 |
| `--ts-primary` | `--ts-surface-1` | 4.5 | 5.32 | 9.29 |
| `--ts-primary` | `--ts-surface-2` | 4.5 | 5.09 | 8.50 |
| `--ts-primary` | `--ts-surface-3` | 4.5 | 4.83 | 7.66 |
| `--ts-primary` | `--ts-surface-hover` | 4.5 | 4.83 | 7.66 |
| `--ts-primary` | `--ts-surface-active` | 4.5 | 4.52 | 6.94 |
| `--ts-primary` | `--ts-surface-raised` | 4.5 | 5.56 | 7.66 |
| `--ts-primary` | `--ts-surface-overlay` | 4.5 | 5.56 | 7.66 |
| `--ts-primary` | `--ts-surface-inset` | 4.5 | 5.09 | 10.02 |
| `--ts-primary-hover` | `--ts-background` | 4.5 | 6.65 | 11.78 |
| `--ts-primary-hover` | `--ts-surface-1` | 4.5 | 6.36 | 10.93 |
| `--ts-primary-hover` | `--ts-surface-2` | 4.5 | 6.09 | 10.00 |
| `--ts-primary-hover` | `--ts-surface-3` | 4.5 | 5.77 | 9.01 |
| `--ts-primary-hover` | `--ts-surface-hover` | 4.5 | 5.77 | 9.01 |
| `--ts-primary-hover` | `--ts-surface-active` | 4.5 | 5.41 | 8.16 |
| `--ts-primary-hover` | `--ts-surface-raised` | 4.5 | 6.65 | 9.01 |
| `--ts-primary-hover` | `--ts-surface-overlay` | 4.5 | 6.65 | 9.01 |
| `--ts-primary-hover` | `--ts-surface-inset` | 4.5 | 6.09 | 11.78 |
| `--ts-primary-soft-foreground` | `--ts-primary-soft` | 4.5 | 6.75 | 7.38 |
| `--ts-primary-soft-foreground` | `--ts-primary-soft-hover` | 4.5 | 6.28 | 6.62 |
| `--ts-primary-soft-foreground` | `--ts-background` | 4.5 | 7.67 | 9.01 |
| `--ts-primary-soft-foreground` | `--ts-surface-1` | 4.5 | 7.33 | 8.35 |
| `--ts-primary-soft-foreground` | `--ts-surface-2` | 4.5 | 7.02 | 7.64 |
| `--ts-primary-soft-foreground` | `--ts-surface-raised` | 4.5 | 7.67 | 6.89 |
| **Status ink** | | | | |
| `--ts-success-ink` | `--ts-success-soft` | 4.5 | 4.92 | 6.57 |
| `--ts-success-ink` | `--ts-success-soft-hover` | 4.5 | 4.56 | 5.92 |
| `--ts-success-ink` | `--ts-background` | 4.5 | 5.72 | 7.99 |
| `--ts-success-ink` | `--ts-surface-1` | 4.5 | 5.47 | 7.41 |
| `--ts-success-ink` | `--ts-surface-2` | 4.5 | 5.24 | 6.79 |
| `--ts-success-ink` | `--ts-surface-raised` | 4.5 | 5.72 | 6.12 |
| `--ts-warning-ink` | `--ts-warning-soft` | 4.5 | 4.84 | 7.97 |
| `--ts-warning-ink` | `--ts-warning-soft-hover` | 4.5 | 4.51 | 7.26 |
| `--ts-warning-ink` | `--ts-background` | 4.5 | 5.52 | 9.50 |
| `--ts-warning-ink` | `--ts-surface-1` | 4.5 | 5.27 | 8.81 |
| `--ts-warning-ink` | `--ts-surface-2` | 4.5 | 5.05 | 8.07 |
| `--ts-warning-ink` | `--ts-surface-raised` | 4.5 | 5.52 | 7.27 |
| `--ts-danger-ink` | `--ts-danger-soft` | 4.5 | 5.39 | 5.47 |
| `--ts-danger-ink` | `--ts-danger-soft-hover` | 4.5 | 4.99 | 4.91 |
| `--ts-danger-ink` | `--ts-background` | 4.5 | 6.18 | 6.49 |
| `--ts-danger-ink` | `--ts-surface-1` | 4.5 | 5.91 | 6.02 |
| `--ts-danger-ink` | `--ts-surface-2` | 4.5 | 5.66 | 5.51 |
| `--ts-danger-ink` | `--ts-surface-raised` | 4.5 | 6.18 | 4.97 |
| `--ts-info-ink` | `--ts-info-soft` | 4.5 | 5.66 | 5.73 |
| `--ts-info-ink` | `--ts-info-soft-hover` | 4.5 | 5.29 | 5.13 |
| `--ts-info-ink` | `--ts-background` | 4.5 | 6.47 | 6.99 |
| `--ts-info-ink` | `--ts-surface-1` | 4.5 | 6.18 | 6.48 |
| `--ts-info-ink` | `--ts-surface-2` | 4.5 | 5.92 | 5.98 |
| `--ts-info-ink` | `--ts-surface-raised` | 4.5 | 6.47 | 5.34 |
| `--ts-byoc-soft-foreground` | `--ts-byoc-soft` | 4.5 | 6.21 | 6.57 |
| `--ts-byoc-soft-foreground` | `--ts-background` | 4.5 | 6.78 | 7.74 |
| `--ts-byoc-soft-foreground` | `--ts-surface-1` | 4.5 | 6.48 | 7.18 |
| `--ts-byoc-soft-foreground` | `--ts-surface-2` | 4.5 | 6.21 | 6.57 |
| `--ts-byoc-soft-foreground` | `--ts-surface-raised` | 4.5 | 6.78 | 5.92 |
| `--ts-chart-axis` | `--ts-background` | 4.5 | 5.66 | 6.53 |
| `--ts-chart-axis` | `--ts-surface-1` | 4.5 | 5.41 | 6.05 |
| `--ts-chart-axis` | `--ts-surface-2` | 4.5 | 5.18 | 5.54 |
| `--ts-chart-axis` | `--ts-surface-raised` | 4.5 | 5.66 | 4.99 |
| **Ink on solid fills** | | | | |
| `--ts-primary-foreground` | `--ts-primary` | 4.5 | 5.56 | 10.02 |
| `--ts-primary-foreground` | `--ts-primary-hover` | 4.5 | 6.65 | 11.78 |
| `--ts-primary-foreground` | `--ts-primary-active` | 4.5 | 7.67 | 9.01 |
| `--ts-success-foreground` | `--ts-success-solid` | 4.5 | 5.72 | 8.24 |
| `--ts-warning-foreground` | `--ts-warning` | 4.5 | 5.90 | 10.10 |
| `--ts-warning-foreground` | `--ts-warning-solid` | 4.5 | 5.90 | 10.10 |
| `--ts-danger-foreground` | `--ts-danger` | 4.5 | 4.59 | 4.77 |
| `--ts-danger-foreground` | `--ts-danger-solid` | 4.5 | 5.59 | 4.77 |
| `--ts-danger-foreground` | `--ts-danger-ink` | 4.5 | 6.18 | 6.49 |
| `--ts-info-foreground` | `--ts-info` | 4.5 | 4.57 | 5.31 |
| `--ts-info-foreground` | `--ts-info-solid` | 4.5 | 4.57 | 5.31 |
| `--ts-byoc-foreground` | `--ts-byoc` | 4.5 | 6.78 | 7.74 |
| `--ts-byoc-foreground` | `--ts-byoc-strong` | 4.5 | 14.44 | 14.95 |
| `--ts-text-inverse` | `--ts-surface-inverse` | 4.5 | 14.44 | 14.95 |
| `--ts-on-dark-text` | `--ts-brand-night` | 4.5 | 18.42 | 18.42 |
| `--ts-text-on-solid` | `--ts-primary` | 4.5 | 5.56 | 10.02 |
| `--ts-text-on-solid` | `--ts-success-solid` | 4.5 | 5.72 | 8.24 |
| `--ts-text-on-solid` | `--ts-danger` | 4.5 | 4.59 | 4.77 |
| `--ts-text-on-solid` | `--ts-danger-solid` | 4.5 | 5.59 | 4.77 |
| `--ts-text-on-solid` | `--ts-info` | 4.5 | 4.57 | 5.31 |
| `--ts-text-on-solid` | `--ts-info-solid` | 4.5 | 4.57 | 5.31 |
| `--ts-text-on-solid` | `--ts-byoc` | 4.5 | 6.78 | 7.74 |
| `--ts-text-on-primary` | `--ts-primary` | 4.5 | 5.56 | 10.02 |
| `--ts-text-on-primary` | `--ts-primary-hover` | 4.5 | 6.65 | 11.78 |
| `--ts-text-on-primary` | `--ts-primary-active` | 4.5 | 7.67 | 9.01 |
| `--ts-text-on-warning` | `--ts-warning` | 4.5 | 5.90 | 10.10 |
| `--ts-text-on-warning` | `--ts-warning-solid` | 4.5 | 5.90 | 10.10 |
| **Disabled text (WCAG-exempt; held to 3:1)** | | | | |
| `--ts-text-disabled` | `--ts-background` | 3 | 3.73 | 4.66 |
| `--ts-text-disabled` | `--ts-surface-1` | 3 | 3.56 | 4.32 |
| `--ts-text-disabled` | `--ts-surface-2` | 3 | 3.41 | 3.99 |
| `--ts-text-disabled` | `--ts-surface-3` | 3 | 3.23 | 3.56 |
| `--ts-text-disabled` | `--ts-surface-hover` | 3 | 3.23 | 3.56 |
| `--ts-text-disabled` | `--ts-surface-active` | 3 | 3.03 | 3.23 |
| `--ts-text-disabled` | `--ts-surface-raised` | 3 | 3.73 | 3.56 |
| `--ts-text-disabled` | `--ts-surface-overlay` | 3 | 3.73 | 3.56 |
| `--ts-text-disabled` | `--ts-surface-inset` | 3 | 3.41 | 4.66 |
| **Non-text UI (3:1)** | | | | |
| `--ts-ring` | `--ts-background` | 3 | 5.56 | 10.02 |
| `--ts-ring` | `--ts-surface-1` | 3 | 5.32 | 9.29 |
| `--ts-ring` | `--ts-surface-2` | 3 | 5.09 | 8.50 |
| `--ts-ring` | `--ts-surface-3` | 3 | 4.83 | 7.66 |
| `--ts-ring` | `--ts-surface-active` | 3 | 4.52 | 6.94 |
| `--ts-ring` | `--ts-surface-raised` | 3 | 5.56 | 7.66 |
| `--ts-ring` | `--ts-surface-overlay` | 3 | 5.56 | 7.66 |
| `--ts-ring` | `--ts-surface-inset` | 3 | 5.09 | 10.02 |
| `--ts-ring` | `--ts-primary-soft` | 3 | 4.90 | 8.21 |
| `--ts-input` | `--ts-background` | 3 | 3.73 | 4.66 |
| `--ts-input` | `--ts-surface-1` | 3 | 3.56 | 4.32 |
| `--ts-input` | `--ts-surface-2` | 3 | 3.41 | 3.99 |
| `--ts-input` | `--ts-surface-3` | 3 | 3.23 | 3.56 |
| `--ts-input` | `--ts-surface-active` | 3 | 3.03 | 3.23 |
| `--ts-input` | `--ts-surface-raised` | 3 | 3.73 | 3.56 |
| `--ts-input` | `--ts-surface-overlay` | 3 | 3.73 | 3.56 |
| `--ts-input` | `--ts-surface-inset` | 3 | 3.41 | 4.66 |
| `--ts-input` | `--ts-primary-soft` | 3 | 3.28 | 3.82 |
| `--ts-input-hover` | `--ts-background` | 3 | 5.66 | 6.53 |
| `--ts-input-hover` | `--ts-surface-1` | 3 | 5.41 | 6.05 |
| `--ts-input-hover` | `--ts-surface-2` | 3 | 5.18 | 5.54 |
| `--ts-input-hover` | `--ts-surface-3` | 3 | 4.91 | 4.99 |
| `--ts-input-hover` | `--ts-surface-active` | 3 | 4.60 | 4.52 |
| `--ts-input-hover` | `--ts-surface-raised` | 3 | 5.66 | 4.99 |
| `--ts-input-hover` | `--ts-surface-overlay` | 3 | 5.66 | 4.99 |
| `--ts-input-hover` | `--ts-surface-inset` | 3 | 5.18 | 6.53 |
| `--ts-input-hover` | `--ts-primary-soft` | 3 | 4.98 | 5.35 |
| `--ts-primary` | `--ts-background` | 3 | 5.56 | 10.02 |
| `--ts-primary` | `--ts-surface-1` | 3 | 5.32 | 9.29 |
| `--ts-scrollbar-thumb` | `--ts-background` | 3 | 3.73 | 4.66 |
| `--ts-scrollbar-thumb` | `--ts-surface-1` | 3 | 3.56 | 4.32 |
| `--ts-scrollbar-thumb` | `--ts-surface-2` | 3 | 3.41 | 3.99 |
| `--ts-scrollbar-thumb` | `--ts-surface-3` | 3 | 3.23 | 3.56 |
| `--ts-scrollbar-thumb` | `--ts-surface-hover` | 3 | 3.23 | 3.56 |
| `--ts-scrollbar-thumb` | `--ts-surface-active` | 3 | 3.03 | 3.23 |
| `--ts-scrollbar-thumb` | `--ts-surface-raised` | 3 | 3.73 | 3.56 |
| `--ts-scrollbar-thumb` | `--ts-surface-overlay` | 3 | 3.73 | 3.56 |
| `--ts-scrollbar-thumb` | `--ts-surface-inset` | 3 | 3.41 | 4.66 |
| `--ts-success-dot` | `--ts-background` | 3 | 5.72 | 8.24 |
| `--ts-success-dot` | `--ts-surface-1` | 3 | 5.47 | 7.64 |
| `--ts-success-dot` | `--ts-surface-2` | 3 | 5.24 | 6.99 |
| `--ts-success-dot` | `--ts-surface-raised` | 3 | 5.72 | 6.30 |
| `--ts-warning-dot` | `--ts-background` | 3 | 5.52 | 9.50 |
| `--ts-warning-dot` | `--ts-surface-1` | 3 | 5.27 | 8.81 |
| `--ts-warning-dot` | `--ts-surface-2` | 3 | 5.05 | 8.07 |
| `--ts-warning-dot` | `--ts-surface-raised` | 3 | 5.52 | 7.27 |
| `--ts-danger-dot` | `--ts-background` | 3 | 5.59 | 4.77 |
| `--ts-danger-dot` | `--ts-surface-1` | 3 | 5.34 | 4.43 |
| `--ts-danger-dot` | `--ts-surface-2` | 3 | 5.12 | 4.05 |
| `--ts-danger-dot` | `--ts-surface-raised` | 3 | 5.59 | 3.65 |
| `--ts-info-dot` | `--ts-background` | 3 | 4.57 | 5.31 |
| `--ts-info-dot` | `--ts-surface-1` | 3 | 4.37 | 4.92 |
| `--ts-info-dot` | `--ts-surface-2` | 3 | 4.19 | 4.51 |
| `--ts-info-dot` | `--ts-surface-raised` | 3 | 4.57 | 4.06 |
| `--ts-idle-dot` | `--ts-background` | 3 | 3.73 | 4.66 |
| `--ts-idle-dot` | `--ts-surface-1` | 3 | 3.56 | 4.32 |
| `--ts-idle-dot` | `--ts-surface-2` | 3 | 3.41 | 3.99 |
| `--ts-idle-dot` | `--ts-surface-raised` | 3 | 3.73 | 3.56 |
| `--ts-idle` | `--ts-background` | 3 | 3.73 | 4.66 |
| `--ts-idle` | `--ts-surface-1` | 3 | 3.56 | 4.32 |
| `--ts-idle` | `--ts-surface-2` | 3 | 3.41 | 3.99 |
| `--ts-idle` | `--ts-surface-raised` | 3 | 3.73 | 3.56 |
| `--ts-series-1` | `--ts-background` | 3 | 5.56 | 10.02 |
| `--ts-series-1` | `--ts-surface-1` | 3 | 5.32 | 9.29 |
| `--ts-series-1` | `--ts-surface-raised` | 3 | 5.56 | 7.66 |
| `--ts-series-2` | `--ts-background` | 3 | 6.78 | 7.74 |
| `--ts-series-2` | `--ts-surface-1` | 3 | 6.48 | 7.18 |
| `--ts-series-2` | `--ts-surface-raised` | 3 | 6.78 | 5.92 |
| `--ts-series-3` | `--ts-background` | 3 | 5.52 | 10.35 |
| `--ts-series-3` | `--ts-surface-1` | 3 | 5.27 | 9.60 |
| `--ts-series-3` | `--ts-surface-raised` | 3 | 5.52 | 7.92 |
| `--ts-series-4` | `--ts-background` | 3 | 5.45 | 6.65 |
| `--ts-series-4` | `--ts-surface-1` | 3 | 5.20 | 6.17 |
| `--ts-series-4` | `--ts-surface-raised` | 3 | 5.45 | 5.09 |
| `--ts-series-5` | `--ts-background` | 3 | 5.72 | 8.24 |
| `--ts-series-5` | `--ts-surface-1` | 3 | 5.47 | 7.64 |
| `--ts-series-5` | `--ts-surface-raised` | 3 | 5.72 | 6.30 |
| `--ts-series-6` | `--ts-background` | 3 | 4.94 | 7.36 |
| `--ts-series-6` | `--ts-surface-1` | 3 | 4.72 | 6.82 |
| `--ts-series-6` | `--ts-surface-raised` | 3 | 4.94 | 5.63 |
| `--ts-series-hover` | `--ts-background` | 3 | 6.65 | 11.78 |
| `--ts-series-hover` | `--ts-surface-1` | 3 | 6.36 | 10.93 |
| `--ts-series-hover` | `--ts-surface-raised` | 3 | 6.65 | 9.01 |
| `--ts-data-4` | `--ts-background` | 3 | 5.45 | 6.65 |
| `--ts-data-4` | `--ts-surface-1` | 3 | 5.20 | 6.17 |
| `--ts-data-4` | `--ts-surface-raised` | 3 | 5.45 | 5.09 |
| `--ts-data-6` | `--ts-background` | 3 | 4.94 | 7.36 |
| `--ts-data-6` | `--ts-surface-1` | 3 | 4.72 | 6.82 |
| `--ts-data-6` | `--ts-surface-raised` | 3 | 4.94 | 5.63 |
| **Decorative lines (no minimum; must differ from the surface)** | | | | |
| `--ts-border-subtle` | `--ts-background` | 1.01 | 1.33 | 1.67 |
| `--ts-border-subtle` | `--ts-surface-1` | 1.01 | 1.27 | 1.55 |
| `--ts-border-subtle` | `--ts-surface-2` | 1.01 | 1.21 | 1.42 |
| `--ts-border-subtle` | `--ts-surface-hover` | 1.01 | 1.15 | 1.28 |
| `--ts-border-subtle` | `--ts-surface-active` | 1.01 | 1.08 | 1.16 |
| `--ts-border-subtle` | `--ts-surface-raised` | 1.01 | 1.33 | 1.28 |
| `--ts-border-subtle` | `--ts-surface-overlay` | 1.01 | 1.33 | 1.28 |
| `--ts-border-subtle` | `--ts-surface-inset` | 1.01 | 1.21 | 1.67 |
| `--ts-border` | `--ts-background` | 1.01 | 1.52 | 2.05 |
| `--ts-border` | `--ts-surface-1` | 1.01 | 1.45 | 1.90 |
| `--ts-border` | `--ts-surface-2` | 1.01 | 1.39 | 1.74 |
| `--ts-border` | `--ts-surface-hover` | 1.01 | 1.32 | 1.57 |
| `--ts-border` | `--ts-surface-active` | 1.01 | 1.24 | 1.42 |
| `--ts-border` | `--ts-surface-raised` | 1.01 | 1.52 | 1.57 |
| `--ts-border` | `--ts-surface-overlay` | 1.01 | 1.52 | 1.57 |
| `--ts-border` | `--ts-surface-inset` | 1.01 | 1.39 | 2.05 |
| `--ts-border-hover` | `--ts-background` | 1.01 | 1.91 | 2.89 |
| `--ts-border-hover` | `--ts-surface-1` | 1.01 | 1.83 | 2.68 |
| `--ts-border-hover` | `--ts-surface-2` | 1.01 | 1.75 | 2.47 |
| `--ts-border-hover` | `--ts-surface-hover` | 1.01 | 1.66 | 2.21 |
| `--ts-border-hover` | `--ts-surface-active` | 1.01 | 1.56 | 2.00 |
| `--ts-border-hover` | `--ts-surface-raised` | 1.01 | 1.91 | 2.21 |
| `--ts-border-hover` | `--ts-surface-overlay` | 1.01 | 1.91 | 2.21 |
| `--ts-border-hover` | `--ts-surface-inset` | 1.01 | 1.75 | 2.89 |
| `--ts-chart-grid` | `--ts-background` | 1.01 | 1.33 | 1.67 |
| `--ts-chart-grid` | `--ts-surface-1` | 1.01 | 1.27 | 1.55 |
| `--ts-chart-grid` | `--ts-surface-2` | 1.01 | 1.21 | 1.42 |
| `--ts-chart-grid` | `--ts-surface-raised` | 1.01 | 1.33 | 1.28 |
| **prefers-contrast: more** | | | | |
| `--ts-scrollbar-thumb` | `--ts-scrollbar-track` | 3 | 14.44 | 14.95 |

---

## Elevation

A rule every component follows, and two tests enforce. Every surface is one
of two kinds:

| | Flat — lies on the page | Raised — lifted off it |
|---|---|---|
| **Examples** | a table, a flat card, a code block, an empty state | a card, a toast, a dialog, a resting control (secondary button, field, boxed choice) |
| **Background** | a page surface: `--ts-background`, `--ts-surface-1`, `-2`, `-inset`, or none | `--ts-surface-raised` |
| **Edge** | a border (`--ts-border-width`, `--ts-border`) | a border |
| **Light** | no shadow | a soft navy shadow (`--ts-elevation-*`) |
| **Dark** | no shadow — **the border is the only edge**, because a shadow does not read on near-black | a **lighter surface step** (`--ts-surface-raised` is step 4 against the night's step 1) + a deeper near-black shadow + the 1px inset top highlight, all from `--ts-elevation-*` |

An inverse surface (the tooltip, `--ts-surface-inverse`) is the one
exception: its fill is its edge, at 14:1, so it takes a shadow and no
border.

One declaration is right in both themes. `--ts-elevation-1…4` combine a
`--ts-shadow-*` with `--ts-highlight`, which is `inset 0 1px 0 rgb(255 255 255
/ 0.06)` in dark and a transparent no-op in light, so a component that uses
them cannot leave the highlight out. Components use the elevation tokens and
nothing else:

| Level | For | Components |
|---|---|---|
| `--ts-elevation-1` | resting controls and cards | `timlee-card`, secondary, primary, danger and BYOC buttons, fields, switches, boxed choices, the code block's copy button |
| `--ts-elevation-2` | something lifted above its neighbours | `timlee-card[elevation="raised"]`, a hovered interactive card, toasts, tooltips |
| `--ts-elevation-3` | dialogs and menus | `timlee-dialog` |
| `--ts-elevation-4` | full-screen sheets | — |

The enforcement:

- `styles.test.ts` (`pnpm test`) reads the component stylesheet
  and fails on any `var(--ts-shadow-*)` or `var(--ts-highlight)` used alone,
  and on any `box-shadow` or `--*-shadow` property that is not an elevation
  token, `none`, or a ring (no offset, no blur).
- Elevation and reduced-motion forms were previously checked by Storybook
  stories, which this repository no longer carries; review them by hand.

`base.css` ships `.panel` (flat) and `.raised` (lifted) as the same rule for
page CSS.

| Token | Light | Dark | Purpose |
|---|---|---|---|
| `--ts-shadow-xs` | `0 1px 2px rgb(15 30 50 / 0.06)` | `0 1px 2px rgb(2 6 14 / 0.28)` | the smallest shadow; use through an elevation token |
| `--ts-shadow-sm` | `0 1px 3px rgb(15 30 50 / 0.08), 0 1px 2px rgb(15 30 50 / 0.04)` | `0 1px 3px rgb(2 6 14 / 0.34), 0 1px 2px rgb(2 6 14 / 0.2)` | in `--ts-elevation-1` |
| `--ts-shadow-md` | `0 4px 12px rgb(15 30 50 / 0.1)` | `0 4px 12px rgb(2 6 14 / 0.42)` | in `--ts-elevation-2` |
| `--ts-shadow-lg` | `0 12px 28px rgb(15 30 50 / 0.14)` | `0 12px 28px rgb(2 6 14 / 0.5)` | in `--ts-elevation-3` |
| `--ts-shadow-xl` | `0 30px 80px rgb(15 30 50 / 0.2)` | `0 30px 80px rgb(2 6 14 / 0.6)` | in `--ts-elevation-4` |
| `--ts-highlight` | `inset 0 1px 0 transparent` | `inset 0 1px 0 rgb(255 255 255 / 0.06)` | the 1px lit top edge of a raised surface in dark; a no-op in light |
| `--ts-elevation-1` | `--ts-shadow-sm` + `--ts-highlight` | the same, dark inks | level 1 |
| `--ts-elevation-2` | `--ts-shadow-md` + `--ts-highlight` | the same, dark inks | level 2 (`.raised`) |
| `--ts-elevation-3` | `--ts-shadow-lg` + `--ts-highlight` | the same, dark inks | level 3 |
| `--ts-elevation-4` | `--ts-shadow-xl` + `--ts-highlight` | the same, dark inks | level 4 |

The dark inks used to be transparent ("never a shadow in dark"). A shadow on
its own still barely reads against the night, which is why the lighter step
and the highlight carry the lift; but a near-black shadow at four to six
times the light alpha deepens the edge of the lighter surface and darkens
whatever a menu or dialog falls across, so a raised surface now has all
three in dark, as the rule asks.

---

## Type

Helvetica carries the interface, prose and display tiers; code keeps the
platform monospace. It matches the default font of the apps Timlee generates
(`workspace/packages/design`), so the site and its output read as one product.

**UI steps stay fixed** — a 13px table label that grows with the viewport is a
bug. **Display steps are fluid**, linear between a 360px and a 1280px
viewport. Their ratio grows from a minor third to a perfect fourth, so the
hierarchy stays useful on a phone and gains contrast on a desktop without a
70px headline shouting on a 360px screen.

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-font-sans` | `Helvetica, "Helvetica Neue", Arial, "Noto Sans", sans-serif` | UI, prose and headings |
| `--ts-font-mono` | `ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace` | code, keys, IDs |
| `--ts-font-display` | `var(--ts-font-sans)` | the fluid display tier (`.display`) |

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-text-xs` | `0.75rem` | 12px: dense table cells, eyebrows |
| `--ts-text-sm` | `0.8125rem` | 13px: secondary labels |
| `--ts-text-base` | `0.875rem` | 14px: app body |
| `--ts-text-md` | `0.9375rem` | 15px: buttons, marketing body |
| `--ts-text-lg` | `1rem` | 16px: lead paragraphs |
| `--ts-text-xl` | `1.125rem` | 18px: section subheads |
| `--ts-text-2xl` | `1.375rem` | 22px: card titles |
| `--ts-text-3xl` | `1.75rem` | 28px: page titles |
| `--ts-text-4xl` | `clamp(1.75rem, 1.505rem + 1.087vw, 2.375rem)` | 28 → 38px between a 360 and a 1280px viewport |
| `--ts-text-5xl` | `clamp(2.125rem, 1.734rem + 1.739vw, 3.125rem)` | 34 → 50px |
| `--ts-text-6xl` | `clamp(2.5rem, 1.864rem + 2.826vw, 4.125rem)` | 40 → 66px: the hero |

Each step carries leading and tracking rather than asking a consumer to
reconstruct the pair. Small text opens slightly; display text closes as its
lines become one shape.

| Step | Leading | Tracking |
|---|---|---|
| `xs` | `--ts-leading-xs: 1.5` | `--ts-tracking-xs: 0.01em` |
| `sm` | `--ts-leading-sm: 1.5` | `--ts-tracking-sm: 0.005em` |
| `base` | `--ts-leading-base: 1.5` | `--ts-tracking-base: 0` |
| `md` | `--ts-leading-md: 1.5` | `--ts-tracking-md: 0` |
| `lg` | `--ts-leading-lg: 1.6` | `--ts-tracking-lg: 0` |
| `xl` | `--ts-leading-xl: 1.45` | `--ts-tracking-xl: -0.005em` |
| `2xl` | `--ts-leading-2xl: 1.3` | `--ts-tracking-2xl: -0.01em` |
| `3xl` | `--ts-leading-3xl: 1.2` | `--ts-tracking-3xl: -0.015em` |
| `4xl` | `--ts-leading-4xl: 1.15` | `--ts-tracking-4xl: -0.02em` |
| `5xl` | `--ts-leading-5xl: 1.1` | `--ts-tracking-5xl: -0.02em` |
| `6xl` | `--ts-leading-6xl: 1.05` | `--ts-tracking-6xl: -0.025em` |

The role aliases remain for rules that do not name a step directly:

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-leading-none` | `1` | single-line controls, badges and icons |
| `--ts-leading-display` | `var(--ts-leading-6xl)` | `.display` |
| `--ts-leading-tight` | `var(--ts-leading-3xl)` | headings |
| `--ts-leading-snug` | `1.4` | wrapping UI text |
| `--ts-leading-normal` | `var(--ts-leading-lg)` | prose |
| `--ts-tracking-display` | `var(--ts-tracking-6xl)` | `.display` |
| `--ts-tracking-tight` | `var(--ts-tracking-4xl)` | headings |
| `--ts-tracking-normal` | `0` | body |
| `--ts-tracking-wide` | `0.05em` | small uppercase labels |
| `--ts-measure` | `65ch` | prose (`.measure`) |
| `--ts-measure-narrow` | `48ch` | captions and cards (`.measure-narrow`) |
| `--ts-measure-heading` | `22ch` | display headings |

`--ts-leading-none` is for a box that holds exactly one line and must be as
tall as its text and no taller — a badge, a button label, an icon glyph, an
avatar initial. Running text never takes it: a second line would collide with
the first. Five rules in `components.css` hand-write `line-height: 1` today.

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-text-code` | `0.875em` | inline `<code>` in running text; em-relative so it scales with its context |

Inline `<code>` in running text takes `--ts-text-code` with
`font-family: var(--ts-font-mono)`. It is in em, not rem, on purpose: a
monospace face sets visibly larger than the proportional text around it at the
same nominal size, and code inside a caption or a heading should scale with
that caption or heading. Three branches had hand-written 0.875em and 0.9em;
this is the one value. Code *blocks* (`<pre>`, `timlee-code-block`) do not
use it: they sit on their own surface and take the fixed UI steps.

Utilities in `base.css`: `.display` (display face, 1.05 leading, −0.025em
tracking), `.measure`, `.measure-narrow`, `.eyebrow`, `.mono`, `.num`.

### A system face

Helvetica ships with macOS and iOS; elsewhere the stack falls to Arial, which
shares its metrics, so layout does not shift between platforms and no font
file is downloaded. Generated apps can pick other faces in the theme editor;
those are self-hosted npm packages bundled into the app, never fetched from a
third party.

---

## Space and rhythm

4px base, with the fine steps real components want (2, 6, 10, 14, 18, 22,
26px). The alternative to having them in the scale is a hardcoded pixel value
in a component file.

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-space-0` | `0` | 0px |
| `--ts-space-0-5` | `0.125rem` | 2px |
| `--ts-space-1` | `0.25rem` | 4px |
| `--ts-space-1-5` | `0.375rem` | 6px |
| `--ts-space-2` | `0.5rem` | 8px |
| `--ts-space-2-5` | `0.625rem` | 10px |
| `--ts-space-3` | `0.75rem` | 12px |
| `--ts-space-3-5` | `0.875rem` | 14px |
| `--ts-space-4` | `1rem` | 16px |
| `--ts-space-4-5` | `1.125rem` | 18px |
| `--ts-space-5` | `1.25rem` | 20px |
| `--ts-space-5-5` | `1.375rem` | 22px |
| `--ts-space-6` | `1.5rem` | 24px |
| `--ts-space-6-5` | `1.625rem` | 26px |
| `--ts-space-7` | `1.75rem` | 28px |
| `--ts-space-8` | `2rem` | 32px |
| `--ts-space-10` | `2.5rem` | 40px |
| `--ts-space-12` | `3rem` | 48px |
| `--ts-space-16` | `4rem` | 64px |
| `--ts-space-20` | `5rem` | 80px |
| `--ts-space-24` | `6rem` | 96px |

Components snap to these steps; an off-scale value is a bug to round, not a
reason for a new step. The last one-offs in the component stylesheets went
to their nearest step: the textarea's 88px minimum to 96px
(`--ts-space-24`, four lines of the field's text), a badge's 5px hit-area
extension to 6px (`--ts-space-1-5`, a 28px target), and the 2px and 4px
offsets to `--ts-space-0-5` and `--ts-space-1`. An offset that is really a
line's width says so: a tab overlapping its tablist's rule is
`calc(var(--ts-border-width) * -1)`, and a glyph centred on the first line
of text computes its offset from the type tokens,
`(var(--ts-text-base) * var(--ts-leading-normal) - glyph) / 2`, instead of
a hand-tuned `0.2em` that drifts when the leading changes.

**Page rhythm is a separate scale and must not be expressed in
`--ts-space-*`.** The gap between marketing sections should grow with the
viewport; the padding inside a card should not. Utilities: `.section`,
`.section.sm`, `.section.lg`.

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-section-sm` | `clamp(4.5rem, 3.56rem + 4.17vw, 6rem)` | 72 → 96px between marketing sections |
| `--ts-section` | `clamp(5rem, 3.43rem + 6.96vw, 7.5rem)` | 80 → 120px |
| `--ts-section-lg` | `clamp(5.5rem, 3.22rem + 10.14vw, 8.75rem)` | 88 → 140px |

---

## Radius

**6px is the workhorse.** The larger steps are for panels and sheets only.
Nothing here is bubbly: heavily rounded corners read as consumer software,
and this is a building tool.

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-radius-sm` | `4px` | small buttons, focus outline corners |
| `--ts-radius` | `6px` | the workhorse: buttons, inputs, badges, menu items |
| `--ts-radius-md` | `8px` | large buttons, cards, panels |
| `--ts-radius-lg` | `10px` | raised panels (`.raised`) |
| `--ts-radius-xl` | `14px` | dialogs |
| `--ts-radius-2xl` | `16px` | sheets |
| `--ts-radius-full` | `999px` | pills, avatars, switches |

---

## Motion

Prefer the **semantic easing roles** over the literal curves: "this element is
entering" survives a change of curve; `cubic-bezier(0.16, 1, 0.3, 1)` does not.
Entrances decelerate into place; exits accelerate away and may be quicker,
because nobody waits to watch something leave.

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-duration-instant` | `70ms` | state flips that should feel immediate (press) |
| `--ts-duration-fast` | `120ms` | hover, press |
| `--ts-duration` | `180ms` | the default |
| `--ts-duration-slow` | `320ms` | panels, sheets |
| `--ts-duration-slower` | `600ms` | page-level reveals |
| `--ts-duration-pulse` | `1000ms` | half a cycle of a looping indicator under reduced motion: a two-second breath, far under three flashes a second |
| `--ts-ease-entrance` | `cubic-bezier(0.16, 1, 0.3, 1)` | decelerate into place |
| `--ts-ease-exit` | `cubic-bezier(0.4, 0, 1, 1)` | accelerate away; may be quicker |
| `--ts-ease-standard` | `cubic-bezier(0.22, 0.61, 0.36, 1)` | everything else |
| `--ts-ease-emphasis` | `cubic-bezier(0.65, 0, 0.35, 1)` | symmetrical, for emphasis |
| `--ts-ease` | `var(--ts-ease-standard) → cubic-bezier(0.22, 0.61, 0.36, 1)` | alias of `--ts-ease-standard` |
| `--ts-ease-out` | `var(--ts-ease-entrance) → cubic-bezier(0.16, 1, 0.3, 1)` | alias of `--ts-ease-entrance` |
| `--ts-ease-in-out` | `var(--ts-ease-emphasis) → cubic-bezier(0.65, 0, 0.35, 1)` | alias of `--ts-ease-emphasis` |

### Reduced motion

**No global switch, anywhere.** A universal `animation-duration: 0.01ms
!important` freezes a spinner, a skeleton or a connection pulse after one
0.01 ms cycle — a broken cue rather than a calm one — and can make an
animation more jarring, not less. Two such switches existed, one in
`base.css` and one keyed on `[data-timlee-ready] *` in the component
stylesheet; both are gone. Instead, every rule that moves something states
its reduced form beside it, and the form depends on what the motion is for:

| Motion | Reduced form | Keyframes |
|---|---|---|
| an entrance (a dialog, a toast) | it still fades, so it is clear what appeared, but it no longer travels | `timlee-fade`: opacity only |
| a looping indicator (spinner, skeleton) | it stops moving and **breathes** instead: opacity 1 → 0.5 and back, every two seconds (`--ts-duration-pulse` each way), so "still working" is still said | `timlee-pulse`: opacity only |
| a slide or a nudge (a switch thumb, a pressed button, a growing bar) | it jumps to where it ends; any colour change that comes with it still fades | none |
| a fade (a tooltip) | unchanged: nothing moves | — |

```css
.btn:active {
  transform: translateY(1px);
}

@media (prefers-reduced-motion: reduce) {
  .btn:active {
    transform: none;
  }
}
```

Or start still and opt in to the movement, as smooth scrolling does:
`@media (prefers-reduced-motion: no-preference) { html { scroll-behavior:
smooth; } }`. Either way the two versions sit together, so one cannot be
edited without seeing the other. A spinner keeps its arc while it breathes,
so a still spinner still reads as a spinner; at full strength the arc keeps
3:1 against what it sits on, in both themes.

`styles.test.ts` also rejects
any `0.01ms` or `[data-timlee-ready] *` rule in the component stylesheet.

| Motion | Where | Reduced form |
|---|---|---|
| button press nudge | `base.css` `.btn:active` | no nudge |
| smooth scrolling | `base.css` `html` | opt-in under `no-preference` |
| spinners | `spinner.css` `timlee-spinner`, `button.css` `.timlee-button__spinner` | the arc stays still and breathes |
| skeleton shimmer | `empty-state.css` `timlee-skeleton` | no sweep; the placeholder breathes, in either direction |
| progress bar growth | `spinner.css` `timlee-progress` | the bar takes its new length at once |
| switch thumb | `switch.css` | the thumb jumps; the track colour still fades |
| tooltip | `tooltip.css` | unchanged: an opacity fade moves nothing |
| dialog and toast entrances | `dialog.css`, `toast.css` | fade only, no rise |

---

## Layering

| Token | Value (both themes) | Purpose |
|---|---|---|
| `--ts-z-base` | `0` | in-flow content |
| `--ts-z-sticky` | `40` | sticky nav and table headers |
| `--ts-z-dropdown` | `60` | menus, popovers |
| `--ts-z-overlay` | `80` | scrims |
| `--ts-z-modal` | `100` | dialogs |
| `--ts-z-toast` | `120` | toasts, the skip link |

---

## Aliases

### Layer 3 short names

`--teal`, `--teal-dark`, `--teal-glow`, `--navy`, `--night`, `--slate`,
`--text`, `--muted`, `--border`, `--tint`, `--stripe`, `--error`, `--error-bg`,
`--series`, `--series-hover`, `--series-track`, `--good`, `--warning`,
`--warning-ink`, `--warning-track` and `--mono` are brand-named aliases kept
for the marketing CSS; each is one `var()` of the `--ts-*` token of the same
job, so it themes with it. The first-party `<timlee-chat>` is light DOM and consumes the same semantic
tokens as the rest of the page; the retired vendor-specific `--chat-*`
mapping has been removed.

### Deprecated ramp names

The pre-12-step names keep rendering. Use the ramp step or named role in new
work.

| Deprecated token | Use instead | Light | Dark |
|---|---|---|---|
| `--ts-teal-50` | `--ts-accent-1` | `#f5fdfd` | `#0d191a` |
| `--ts-teal-100` | `--ts-accent-2` | `#eafafb` | `#0a2021` |
| `--ts-teal-200` | `--ts-accent-4` | `#c8f0f2` | `#043436` |
| `--ts-teal-300` | `--ts-accent-6` | `#a5dfe2` | `#06484b` |
| `--ts-teal-400` | `--ts-accent-7` | `#8dd0d3` | `#11585c` |
| `--ts-teal-500` | `--ts-accent-8` | `#64b8bc` | `#1e7478` |
| `--ts-teal-600` | `--ts-accent-9` | `#007479` | `#5fd0d0` |
| `--ts-teal-700` | `--ts-accent-10` | `#00676c` | `#7ddee3` |
| `--ts-teal-800` | `--ts-accent-11` | `#005d61` | `#6bc3c8` |
| `--ts-teal-900` | `--ts-accent-12` | `#004144` | `#ade3e5` |
| `--ts-teal-950` | `--ts-accent-12` | `#004144` | `#ade3e5` |
| `--ts-neutral-0` | `--ts-neutral-1` | `#ffffff` | `#0b1424` |
| `--ts-neutral-10` | `light-dark(var(--prim-navy-10), var(--prim-navy-dark-10))` | `#5d6877` | `#8d9baf` |
| `--ts-neutral-11` | `light-dark(var(--prim-navy-11), var(--prim-navy-dark-11))` | `#515c6c` | `#9ca9bd` |
| `--ts-neutral-12` | `light-dark(var(--prim-navy-12), var(--prim-navy-dark-12))` | `#1b2a41` | `#e1e8f3` |
| `--ts-neutral-50` | `--ts-neutral-2` | `#f8fafc` | `#0f1c30` |
| `--ts-neutral-100` | `--ts-neutral-2` | `#f8fafc` | `#0f1c30` |
| `--ts-neutral-150` | `--ts-neutral-3` | `#f3f5f8` | `#14233a` |
| `--ts-neutral-200` | `--ts-neutral-4` | `#eceff4` | `#1a2c45` |
| `--ts-neutral-300` | `--ts-neutral-5` | `#e4e8ee` | `#22334c` |
| `--ts-neutral-400` | `--ts-neutral-8` | `#b4bcc8` | `#526076` |
| `--ts-neutral-500` | `--ts-neutral-9` | `#7c8593` | `#738196` |
| `--ts-neutral-600` | `--ts-neutral-10` | `#5d6877` | `#8d9baf` |
| `--ts-neutral-700` | `--ts-neutral-11` | `#515c6c` | `#9ca9bd` |
| `--ts-neutral-800` | `--ts-neutral-12` | `#1b2a41` | `#e1e8f3` |
| `--ts-neutral-900` | `--ts-neutral-12` | `#1b2a41` | `#e1e8f3` |
| `--ts-neutral-950` | `--ts-neutral-12` | `#1b2a41` | `#e1e8f3` |
| `--ts-surface-inset-soft` | `--ts-surface-inset` | `#f3f5f8` | `#0b1424` |

---

## Rules for consumers

1. **Use layer 2.** Never reach into `--prim-*` from a component. If a job has
   no semantic token, add one here rather than binding a primitive locally.
2. **Accent discipline.** Teal covers **no more than 10% of any viewport** —
   the primary call to action plus one or two live-state indicators. It is never
   a large fill, a section background or a hero wash. An accent covering a third
   of the screen has stopped being an accent.
3. **Never branch a colour on the theme.** No colour override under
   `[data-theme="dark"]` or `prefers-color-scheme` in component or page CSS:
   it only works when the attribute is present, and it is exactly the twin that
   drifts. If a colour differs by theme, it is a token here, declared with
   `light-dark()`. Non-colour switches (which icon shows) may key on the
   scheme, but the server writes `[data-theme]` only for a reader who chose a
   theme, so such a switch also keys on `prefers-color-scheme` under
   `:root:not([data-theme])`, as the theme toggle in `base.css` does.
4. **A re-themed subtree needs a background.** `data-theme` (or
   `color-scheme`) on an element re-themes everything inside it; `base.css`
   gives it `--ts-background` and `--ts-text-1`, at zero specificity.
5. **Fill vs dot.** `--ts-<role>` for a shape with ink on it,
   `--ts-<role>-dot` for a standalone indicator.
6. **Elevation is a rule.** A surface is flat (a page surface, a border, no
   shadow) or raised (`--ts-surface-raised`, a border, `--ts-elevation-*`).
   Never `--ts-shadow-*` alone and never a literal shadow: it would leave out
   the dark highlight. `.panel` and `.raised` in `base.css` are the rule for
   page CSS.
7. **Radius 6px unless the thing is a panel.**
8. **No hardcoded px in component CSS** for space, radius, type or border
   width — those scales exist so that `padding: 9px 16px` stops happening.
9. **Reduced motion beside the motion.** Every rule that moves something states
   its reduced form next to it (see Reduced motion). Never a global switch.
10. **Scrollbars inherit.** Never set `scrollbar-color` on `*`; an inner
    scroller that should be thin says `scrollbar-width: thin` itself.

---

## Verifying a change

Two committed tests guard the colours, and both run in real Chromium with the
shipped stylesheets:

- `web/components/tokens.test.ts` (`pnpm test`) paints every role
  pair in a light and a dark subtree and fails on any pair under its minimum:
  text 4.5:1 on every surface and tint, control boundaries, the focus ring and
  status dots 3:1, disabled text 3:1, and every line distinct from every
  surface it crosses. It names the pair, the theme and the ratio.

The full table and both parity proofs above come from two scripts kept out of
the repository (`check.mjs`, static, both the hex and the OKLCH path;
`browser-check.mjs`, Chromium, WebKit and Firefox). These one-liners catch the
other regressions that matter most:

```sh
# 1. no theme branch: no --ts-* under data-theme or prefers-color-scheme
node -e "const s=require('fs').readFileSync('web/assets/css/tokens.css','utf8').replace(/\/\*[\s\S]*?\*\//g,'');let d=0,c=[],bad=[];for(const m of s.matchAll(/[^{};]+\{|\}|--ts-[\w-]+(?=\s*:)/g)){const t=m[0];if(t==='}'){c.pop();continue}if(t.endsWith('{')){c.push(t);continue}if(c.some(x=>/data-theme|prefers-color-scheme/.test(x)))bad.push(t)}console.log(bad.length?'THEME BRANCH: '+bad.join(' '):'no theme branches')"

# 2. no literal colour outside layer 1
awk '/LAYER 2a/{p=1} p' web/assets/css/tokens.css | sed 's|/\*.*\*/||' \
  | grep -nE '#[0-9a-fA-F]{3,8}\b|\b(rgba?|hsla?|oklch|oklab)\(' || echo 'no literal colours'

# 3. it still builds — into a private directory, never the shared web/dist
go run ./cmd/timlee-build -out .local/tokens-check
```

### Size

Brotli is what the server sends; each file measured alone, minified by
`cmd/timlee-build`:

| | Raw | gzip | Brotli |
|---|---:|---:|---:|
| `tokens.css` before | 31,540 B | 5,637 B | 4,495 B |
| `tokens.css` after | 31,125 B | 6,063 B | 4,733 B |
| `base.css` before | 4,719 B | 1,462 B | 1,232 B |
| `base.css` after | 4,376 B | 1,393 B | 1,156 B |
| `app.css` before | 81,468 B | 14,219 B | 11,992 B |
| `app.css` after | 80,710 B | 14,567 B | 12,215 B |

Smaller raw, **+223 B brotli** on the render-blocking stylesheet: the old
second dark block compressed to almost nothing, while a `light-dark()` pair
carries both answers on every line, and the fallback restates the light ones.
That is the price of parity by construction, nested theming and a fallback
that is never a broken page.
