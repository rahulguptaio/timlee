# Status

_Last updated: 2026-11-04._

Timlee was forked from an earlier internal project and stripped to what the
product needs: the server-rendered website (`timlee-web`), the control plane
(`timlee-auth`), the dev orchestrator, the asset pipeline and the desktop
shell.

## Done

- Landing page, sign-in (OAuth, magic link, development sign-in), sessions,
  CSRF, account and organisation settings, theme and locale switching.
- Prompt allowlist: `TIMLEE_ALLOWED_EMAILS` on the control plane; the session
  response carries `user.allowed`, and every `/apps` route checks it.
- Apps: list and create (`/apps`), and a workspace per app (`/apps/{slug}`)
  with prompts (live progress over SSE), a code editor, a theme and font
  editor, and a sandboxed live preview on a separate site behind expiring
  capability URLs.
- `internal/appfactory`: Copilot CLI runs per prompt (full tools, owner's
  account, per-prompt credit cap), boundary enforcement, Bazel builds,
  preview publishing, one commit per change in the workspace repository.
- Workspace (`workspace/`): Bazel 9 + rules_js, Svelte 5 CSR app template
  with SEO tags, `@timlee/design` (Web Awesome themed light/dark, Helvetica
  default, self-hosted optional fonts), `@timlee/graphql` client stub, and a
  shared Tauri shell (`node tools/desktop.mjs <slug>`).

## Known gaps (before exposing beyond this laptop)

- **Isolation.** Copilot runs with full tools as the server's user and can
  read its GitHub token, the host filesystem and the network. The git
  boundary is damage control, not a sandbox. Run jobs in a disposable VM or
  container under a dedicated user before accepting prompts from anyone you
  would not hand your laptop to.
- **Tenancy.** Apps have no owner: every allowlisted account sees and edits
  every app, and all previews share one origin (and its storage). Needs the
  backend/database (owner IDs, ACLs on every route, per-app preview hosts).
- **Operations.** Only `timlee-dev` supervises the services: no launchd unit,
  restart policy, log rotation, or backups of `workspace/.git` and the SQLite
  database. If the supervisor is killed hard, its children keep running; stop
  it with Ctrl-C. The workspace repository has no remote.
- **Build gate.** A successful Vite build is the only check; there are no
  `bazel test`, lint or `svelte-check` targets for apps yet. The app build
  runs unsandboxed (`no-sandbox`) because Vite rejects Bazel's sandbox paths.
- **Bazel downloads.** On this network Bazel's JVM cannot reach github.com;
  `node tools/bazel-fetch.mjs` proxies its downloads.
- **Desktop.** App and site desktop builds are unsigned. The site's CSS uses
  nesting and `color-mix()`, so desktop shells need macOS 14 (Safari 17.2) or
  later.
- Settings still include API keys; nothing consumes them yet.
- GraphQL backend and database: not designed yet (`VITE_GRAPHQL_ENDPOINT`).
- Only English ships.
