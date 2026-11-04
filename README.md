# Timlee

Describe an app. Timlee builds it.

Signed-in, allowlisted users type a prompt; the server hands it to the GitHub
Copilot CLI, which edits a generated Svelte app inside the Bazel monorepo in
`workspace/`. Every app is built on one shared design system, so it can be
previewed, edited with follow-up prompts and re-themed.

This repository holds the website and its services. They are Go binaries;
Node.js is only frontend tooling and the Tauri CLI.

| Part | Implementation |
|---|---|
| `cmd/timlee-web` | Server-rendered site: Go `html/template`, light-DOM Web Components, progressive enhancement |
| `cmd/timlee-auth` | Control plane: OAuth, magic links, dev sign-in, sessions, organisations, API keys, prompt allowlist, SQLite |
| `cmd/timlee-dev` | Local orchestrator: builds and supervises auth + web |
| `cmd/timlee-build` | Asset pipeline: esbuild (Go API), hashed files, embedded into `timlee-web` |
| `internal/appfactory` | Creates apps, runs the Copilot CLI per prompt, keeps it inside the app, builds with Bazel, publishes previews, commits |
| `desktop/` | Tauri 2 shell around the hosted site |
| `workspace/` | Bazel monorepo of generated Svelte apps; its own git repository (see [workspace/README.md](workspace/README.md)) |

## Requirements

- Go 1.27 or later.
- Bazelisk (`brew install bazelisk`) for the app workspace.
- GitHub CLI signed in (`gh auth login`) and the Copilot CLI (`copilot`) on
  `PATH`: prompts run with that account and spend its Copilot requests.
- Node.js 22.12+ and pnpm 10 via Corepack (frontend tooling only).
- Chromium for component tests: `corepack pnpm exec playwright install chromium`.
- Rust stable for desktop work.

## Run locally

```sh
corepack pnpm install
go run ./cmd/timlee-dev
```

The orchestrator builds the assets and both services, starts them on
**127.0.0.1 only**, waits for each to be healthy and stops everything on
Ctrl-C.

| Service | Default | Flag |
|---|---|---|
| Website (`timlee-web`) | <http://localhost:5173> | `-web-port` |
| App previews (inside `timlee-web`) | <http://preview.localhost:5174> | web port + 1 |
| Control plane (`timlee-auth`) | <http://127.0.0.1:8788> | `-auth-port` |

`workspace/` is a separate git repository (ignored here). On a new machine,
clone it into `./workspace`, then install its dependencies once:
`cd workspace && corepack pnpm install && node tools/bazel-fetch.mjs`.

Other flags: `-watch` rebuilds assets on change, `-services auth` runs one
service, `-h` lists everything. Secrets are generated once into
`.local/secrets.env`; the SQLite database is `.local/data/timlee.db`.

### Signing in locally

1. Open <http://localhost:5173/sign-in>.
2. Use **development sign-in** (enabled outside production). With the email
   field empty you are signed in as `dev@timlee.local`.
3. `timlee-dev` sets `TIMLEE_ALLOWED_EMAILS=dev@timlee.local` unless you set
   it yourself, so that account may submit prompts.

To use your own address, export `TIMLEE_ALLOWED_EMAILS=you@example.com`
before `go run ./cmd/timlee-dev` and sign in with it (development sign-in, or
a magic link: links are written to `.local/mail/`).

## Building apps

Open <http://localhost:5173/apps>, create an app (optionally with a first
prompt), then work in its workspace:

- **Prompt**: Copilot edits `workspace/apps/<slug>/`; progress streams live.
  Timlee then reverts anything outside the app, builds with Bazel, and commits.
- **Code**: browse and edit the app's files (CodeMirror); Cmd/Ctrl+S saves,
  rebuilds, and commits. `timlee.json` and the theme files are read-only here.
- **Theme**: font and light/dark color overrides of the shared design system,
  validated for contrast, written to the app's `theme.override.css`.
- **Preview**: the last good build, served from a separate site and framed
  sandboxed.

Every change is a commit in `workspace/`'s repository (`git -C workspace log`).

> **Security.** Prompts run the Copilot CLI with `--allow-all-tools` on this
> machine, as your user, with your GitHub token. Anyone allowlisted can make it
> run commands here. Keep the allowlist to people you trust with your laptop,
> keep the services on 127.0.0.1, and see [docs/STATUS.md](docs/STATUS.md)
> before exposing them.

## Prompt allowlist

Only allowlisted accounts may submit prompts.

- `TIMLEE_ALLOWED_EMAILS`: comma-separated, case-insensitive, read by
  `timlee-auth`. **Empty allows nobody.**
- `GET /auth/session` on the control plane returns `user.allowed`.
- `timlee-web` copies it into `render.User.Allowed`; handlers and templates
  use `p.Allowed()` / `{{ if .Allowed }}`. Any route that accepts a prompt must
  check it on the server.

## Configuration

Every variable is listed with its default in [`.env.example`](.env.example).
The main ones: `TIMLEE_ENV`, `TIMLEE_WEB_ADDR`, `TIMLEE_CONTROL_ADDR`,
`TIMLEE_PUBLIC_ORIGIN`, `TIMLEE_CONTROL_ORIGIN`, `SESSION_SECRET`,
`TIMLEE_INTERNAL_TOKEN`, `TIMLEE_DB`, `TIMLEE_ALLOWED_EMAILS`, and the OAuth
client IDs/secrets. In production (`TIMLEE_ENV=production`) the services refuse
to start with development secrets, http origins, dev sign-in or a mail outbox.

## Build and verify

```sh
go build ./... && go vet ./... && go test ./...
corepack pnpm lint && corepack pnpm typecheck && corepack pnpm test
go run ./cmd/timlee-build      # writes web/dist and the embedded copy
```

See [docs/CHECKS.md](docs/CHECKS.md). Desktop: [docs/DESKTOP.md](docs/DESKTOP.md).

## Architecture

```text
browser / desktop webview
          │
          ▼
  timlee-web :5173  ── session lookup, forms ──▶  timlee-auth :8788 ── SQLite
  (SSR pages, assets)                             (sessions, OAuth, allowlist)
```

Contributors and coding agents: read [AGENTS.md](AGENTS.md) first.
