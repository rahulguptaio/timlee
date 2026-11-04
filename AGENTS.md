# Timlee agent guide

This file is the repository-wide operating contract for coding agents and
contributors. Read it before changing code, then the nearest referenced
document for the area you are touching.

## Mission and invariants

Timlee is a website where signed-in, allowlisted users type prompts that the
GitHub Copilot CLI turns into Svelte apps inside the Bazel monorepo in
`workspace/`. Preserve these properties:

- The website (`timlee-web`) and the control plane (`timlee-auth`) remain
  separate services. Both bind loopback by default.
- The website is server-rendered and useful without JavaScript; browser code
  progressively enhances server-rendered HTML.
- Prompts are accepted only from accounts on `TIMLEE_ALLOWED_EMAILS`, checked
  on the server. An empty allowlist allows nobody.
- Session, organisation and API-key boundaries are enforced server-side.
- Production ships Go binaries with templates and assets embedded in
  `timlee-web`. Node.js is tooling only.

Start with [`README.md`](README.md), [`docs/README.md`](docs/README.md),
[`docs/STATUS.md`](docs/STATUS.md) and [`llms.txt`](llms.txt).

## Architecture map

| Area | Primary paths | Responsibility |
|---|---|---|
| Website | `cmd/timlee-web`, `internal/render`, `web/templates` | SSR pages, sign-in relay, settings, theme/locale forms, apps workspace (`apps.go`), preview server (`preview.go`) |
| Control plane | `cmd/timlee-auth`, `internal/authsvc`, `internal/store` | Sessions, OAuth, magic links, organisations, API keys, audit, allowlist, SQLite |
| Configuration | `internal/config` | Every `TIMLEE_*` variable, production validation, allowlist parsing |
| Frontend | `web/assets`, `web/components` | TypeScript islands, light-DOM Web Components, CSS and tokens |
| Localization | `web/locales`, `internal/i18n` | ARB catalogs, locale routing, formatting, RTL |
| App factory | `internal/appfactory` | Create apps from `workspace/templates/app`, run Copilot per prompt, revert changes outside the app, Bazel build, publish previews, commit to the workspace repo, file and theme editing |
| Dev orchestrator | `cmd/timlee-dev`, `internal/devstack` | Build, start and supervise auth + web locally |
| Asset build | `cmd/timlee-build`, `internal/assets` | Hashed assets, manifest, embed tree |
| Desktop | `desktop`, `docs/DESKTOP.md` | Tauri 2 shell around the hosted site |
| Generated apps | `workspace/` | Bazel monorepo (its own git repo). Edit shared parts (`packages/`, `templates/`, `tools/`, `AGENTS.md`) deliberately; never hand-edit `apps/` here |

```text
browser / desktop webview
          │
          ▼
  timlee-web 127.0.0.1:5173 ── GET /auth/session, form relays ──▶ timlee-auth 127.0.0.1:8788 ── SQLite
     │  /apps/{slug}: prompt (SSE progress), code editor, theme editor
     │  internal/appfactory (one operation at a time)
     ├─▶ copilot -p … --allow-all-tools   (cwd: workspace/apps/<slug>, owner's gh token)
     ├─▶ git boundary check + commit       (workspace/.git, hooks disabled)
     ├─▶ bazel build //apps/<slug>:dist    (copied to .local/previews/<slug>)
     ▼
  preview server preview.localhost:5174 (separate site; /<expiry>.<hmac>/<slug>/, framed sandboxed)
```

## The app workspace: invariants

- **One operation at a time** (create, prompt run, file save, theme save):
  `Factory.begin` fails fast with `ErrBusy`. `enforceBoundary` rewinds to the
  operation's starting commit, which is only safe because nothing else commits
  meanwhile.
- **Boundary**: after a run and after every build, anything changed outside
  `apps/<slug>/` is reverted, `.git/config` is restored, and `timlee.json` and
  the generated theme files are restored and regenerated. Git runs with hooks,
  fsmonitor, and user/system config disabled (`gitEnv`, `gitRaw`). This is
  damage control, not a sandbox: Copilot runs with full tools as the server's
  user and can read its GitHub token.
- **Previews** are only reachable with a capability URL issued to an
  allowlisted reader of the workspace page, and only from a different site.
- **Theme values** are validated (catalog fonts, `#rrggbb`, plain decimals,
  WCAG contrast rules from `tokens.json`) before being written into CSS.

## Adding a page to timlee-web

1. **Template**: `web/templates/pages/<name>.gohtml` defining
   `{{ define "content" }}`. The page name is the file name. Layouts live in
   `web/templates/layouts/` (`base` by default, `auth`, `settings`); select one
   with `p.Layout`. Partials (`nav`, `footer`, `logo`, `theme-toggle`, …) live
   in `web/templates/partials/`. Parsed once at startup; a broken template
   stops the process. Add the name to the list in
   `internal/render/render_test.go` (`TestShippedTemplatesParse`).
2. **Handler**: a method on `*server` in a new file in `cmd/timlee-web`.
   Start with `p := s.page(r)` (resolves the session via
   `authClient.User` → `GET /auth/session`, locale, nonce, theme), set
   `p.Title`, `p.Description`, `p.Data`, then
   `s.render(w, r, status, "<name>", p)`.
3. **Route**: register it in `(*server).routes` in `cmd/timlee-web/server.go`
   with a method pattern (`mux.HandleFunc("GET /apps/{slug}", s.app)`), before
   the `"/"` catch-all.
4. **Signed-in pages**: if `!p.SignedIn()` redirect to
   `p.L.SignInPath(r.URL.RequestURI())`; set `p.Robots = "noindex,nofollow"`
   and `Cache-Control: no-store`. Personal app areas belong in `appRoots` in
   `cmd/timlee-web/locale.go` (`/apps` is already there).
5. **Prompts / state-changing POSTs**: check `s.fromThisSite(r)` and
   `validToken(r)` (session-derived CSRF, `cmd/timlee-web/csrf.go`); render
   the hidden field with `csrfInput(csrfToken(r))`. Then require
   `p.Allowed()` (or `s.auth.User(ctx, r).Allowed`) before doing anything.
   `settingsAction` in `settings_actions.go` is the existing wrapper pattern.
6. **Copy**: add keys to an ARB file in `web/locales/en/`; use `t`/`th` in
   templates and `p.L.T` in Go. No hard-coded user-facing text.
7. **Assets**: every file directly in `web/assets/` is an entry point
   (`app.ts` → `app.js`, `app.css`); nested files are modules. Page CSS goes
   in `web/assets/css/` and is `@import`ed from `app.css`; page scripts are
   dynamic `import()`s from `app.ts` gated on markup. Templates reference
   assets only through `{{ asset "name" }}` / `{{ assetIntegrity "name" }}`.
   Components load on demand via `web/components/loader.ts`.
8. **CSP**: `(*server).pageCSP` in `server.go`: `default-src 'none'`, scripts
   and styles from `'self'` or the response nonce (`{{ .Nonce }}`),
   `img-src 'self' data: https:`, `connect-src 'self'` plus the control
   origin, `frame-src 'none'`, `frame-ancestors 'none'`. Never add
   `unsafe-inline`. An iframe preview of generated apps needs an explicit
   `frame-src` for that origin.

## Generated and local files

Do not hand-edit or commit: `web/dist/**`,
`internal/assets/embedded/static/**`, `.local/**`, SQLite databases, `.env*`,
credentials, coverage and test artifacts. `internal/i18n/cldr/tables.go` is
generated (`go generate ./internal/i18n/cldr`). `go run ./cmd/timlee-build`
writes `web/dist` and mirrors it into the embed directory.

## Go rules

- Use the Go version pinned by `go.mod`.
- Reuse packages under `internal/`; do not duplicate config, HTTP, rendering,
  auth, logging, SSE or storage logic.
- MixedCaps with acronym casing (`URL`, `HTTP`, `ID`); no package stutter.
- Pass `context.Context` through request and database boundaries; wrap errors
  with context; return caller-safe HTTP messages and log details separately.
- Use `httpx.WriteJSON` and `httpx.WriteError` for standard responses.
- Keep migrations embedded, ordered and transactional.
- Tenant-scoped storage always filters by organisation ID.
- `/readyz` means traffic can be sent; `/health` means the process is alive.
- Long-lived SSE handlers flush, stop on disconnect, avoid write deadlines
  and are never retried after partial delivery.

## Frontend and SSR rules

- Go `html/template` output is the authoritative page; JavaScript enhances it.
- Keep custom elements in the light DOM; extend `TimleeElement`; clean up
  connection-scoped listeners. Reuse `timlee-*` components before adding one.
- User-facing copy belongs in ARB catalogs.
- Use logical CSS properties and the token layers
  (`--prim-*` → `--ts-*` → component-local variables).
- Never hard-code hashed asset filenames.
- The Tauri shell loads the same hosted site; there is no second frontend.

## Security and privacy rules

- Browser sessions are opaque database-backed IDs (`timlee.sid`, or
  `__Host-timlee.sid` in production). Never put user or role data in cookies.
- Keep OAuth state, nonce, PKCE, callback and redirect validation intact.
- `/internal/*` on the control plane requires `TIMLEE_INTERNAL_TOKEN`.
- Trust `X-Forwarded-*` only with `TIMLEE_TRUST_PROXY=true`.
- Preserve origin/Fetch Metadata checks and session-derived CSRF.
- Do not add `unsafe-inline`, reuse nonces or cache personalised HTML.
- Do not log prompts, cookies, keys, OAuth codes or secrets; log counts,
  codes, opaque IDs and request IDs ([docs/LOGGING.md](docs/LOGGING.md)).

## Checks

```sh
go build ./... && go vet ./... && go test ./...
corepack pnpm lint && corepack pnpm typecheck && corepack pnpm test
go run ./cmd/timlee-build
cargo test --manifest-path desktop/src-tauri/Cargo.toml   # desktop changes
```

Details: [docs/CHECKS.md](docs/CHECKS.md). For visible changes, check light
and dark themes, mobile and desktop widths, and reduced motion.

## Git and review safety

- Do not reset, rebase, amend, force-push or discard user work without
  instruction. Do not commit secrets, databases or `.local/`.
- Conventional Commits, header at most 72 characters.
- Do not touch `workspace/` from changes in this repository.

## Anti-patterns

Do not replace SSR with a client-only app, add a Node production runtime, add
Shadow DOM, bypass tokens or duplicate components, accept a prompt without the
server-side allowlist check, expose the control plane's internal routes, or
log credentials or prompts.
