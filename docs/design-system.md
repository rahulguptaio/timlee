# Timlee design system

The components Timlee's pages are built from live in `web/components/`:
vanilla, light-DOM custom elements wrapped around the native controls the Go
templates emit, plus the chat transcript and composer in
`web/components/chat/`. Tokens live in `web/assets/css/tokens.css`
([design-tokens.md](design-tokens.md)).

> **Development tooling only.** Everything in `package.json` is a
> devDependency. Production ships Go binaries; `go run ./cmd/timlee-build`
> compiles the frontend with esbuild's Go API and no Node is deployed.

## Setup and commands

```sh
corepack pnpm install
corepack pnpm exec playwright install chromium   # once; component tests run in real Chromium
corepack pnpm lint        # biome
corepack pnpm typecheck   # tsc --noEmit, strict
corepack pnpm test        # component unit tests (web/components/vitest.config.ts)
```

Component tests run in Vitest browser mode in Chromium because the behaviour
under test (ElementInternals, the top layer, `:focus-visible`, layout) does not
exist in jsdom. `a11y.test.ts` runs axe against server-shaped markup.

## Adding a component

1. Write `web/components/<name>.ts`: extend `TimleeElement`, register with
   `define('timlee-<name>', Timlee<Name>)`, fire events with
   `this.emit('timlee-<event>', detail)`.
2. Put its styles in `web/components/<name>.css` and import that from
   `components.css`, so server-rendered markup is styled before any script.
3. Add a selector to `BEHAVIOURS` in `web/components/loader.ts` so pages load
   the module only when their markup contains the element.
4. Write `web/components/<name>.test.ts` beside it, starting from the markup
   Go emits (see `server-render.test.ts`).

The components render light DOM. Do not add Shadow DOM: cross-element ARIA
references and native form behaviour depend on light DOM.

### Component CSS contract

Semantic `--ts-*` tokens enter the component layer through the internal
`--mc-*` aliases in `shared.css` or a component-specific property such as
`--button-bg`. Visual declarations never reach back into the global scale,
and states and variants override component properties rather than duplicating
those declarations. The shared bridge keeps repeated primitives compact;
named component properties keep the state API visible and deliberate.
`styles.test.ts` enforces the boundary for every component stylesheet.

## Themes

`data-theme` on `<html>` is decided on the server from the theme cookie
(`POST /theme`, `web/assets/ts/theme.ts`). Without a choice the page follows
the system scheme. Every component must meet WCAG AA in both themes.

## Files

| File | Role |
| --- | --- |
| `web/components/index.ts` | Defines every element at once (tests) |
| `web/components/loader.ts` | Loads only the modules a page's markup needs |
| `web/components/components.css` | Every component stylesheet, imported by `web/assets/app.css` |
| `web/components/vitest.config.ts` | Browser-mode unit tests |
| `tsconfig.json` | Strict, `verbatimModuleSyntax`, `moduleResolution: bundler` |
