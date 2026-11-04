# Timlee desktop app

Timlee for macOS and Windows is a [Tauri 2](https://tauri.app/) shell
(`desktop/`) around the hosted Timlee site. It ships no copy of the UI, starts
no server and has no Node at run time; the Tauri CLI is its only JavaScript
dependency, used for building.

| | |
|---|---|
| Opens | `https://timlee.example/apps` in release builds; `http://localhost:5173` in development builds. `TIMLEE_DESKTOP_ORIGIN` at compile time overrides either |
| Identifier | `io.timlee.desktop` (product name **Timlee**) |
| Signing | Not configured: builds are unsigned until signing certificates exist |

```text
desktop/
  package.json, pnpm-lock.yaml   the Tauri CLI, pinned (outside the pnpm workspace)
  src/                           bundled boot/offline (index.html) and sign-in (signin.html) pages
  src-tauri/
    tauri.conf.json              product, bundle, CSP for the bundled pages, updater endpoint
    capabilities/local.json      the only IPC grant, bundled pages only
    locales/en.arb               the app's own words
    src/lib.rs                   window, navigation policy wiring, menu actions, commands
    src/routes.rs                where each navigation may go (unit-tested)
    src/signin.rs                RFC 8252 loopback sign-in with PKCE (unit-tested)
    src/i18n.rs, menu.rs, updates.rs
  scripts/smoke-macos.sh         macOS smoke test; fake-browser.sh is its stand-in browser
```

## Rules

- Rust owns the native menu, window and single-instance behaviour, the
  updater, the navigation allowlist, IPC and the desktop sign-in handoff.
- Only the configured origin loads in the window; external HTTPS links open in
  the system browser. Identity-provider pages are never embedded.
- Desktop sign-in: the site's `/sign-in/desktop/start` and
  `/sign-in/desktop/complete` routes (`cmd/timlee-web/desktop.go`) hand a
  one-time, PKCE-bound code to the app's loopback listener, which redeems it
  into the webview's cookie jar.
- App pages (`/apps`, `/settings`) have one URL in every language; the app's
  own pages read the `timlee.locale` cookie.

## Development

```sh
corepack pnpm tauri:install     # once: installs the pinned Tauri CLI into desktop/
corepack pnpm dev:desktop       # the Go stack plus `tauri dev`
corepack pnpm tauri:dev         # just the app, against a stack you started
cargo test --manifest-path desktop/src-tauri/Cargo.toml
corepack pnpm tauri:build       # release app for this machine
corepack pnpm tauri:smoke       # macOS smoke test
```

A development build may use `TIMLEE_DESKTOP_BROWSER=<program>` instead of the
system browser; `TIMLEE_DESKTOP_REPORT=1` prints each page the webview renders.
