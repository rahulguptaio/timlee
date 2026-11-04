# Contributing to Timlee

## Setup

Requirements: Go 1.27+, Node.js 22.12+ with pnpm 10 (Corepack), Chromium for
component tests, Rust stable for desktop changes.

```sh
corepack pnpm install --frozen-lockfile
go run ./cmd/timlee-dev
go tool -modfile=tools/hooks/go.mod lefthook install   # once per clone
```

## Change rules

- Keep one coherent concern per commit; use Conventional Commits
  (`type(scope): imperative summary`).
- Do not commit secrets, generated local state, `.env` files or `.local/`.
- Keep production behaviour in Go. Node.js is tooling only.
- Preserve server-rendered and no-JavaScript behaviour.
- Use existing design tokens and Web Components instead of ad-hoc controls.
- Do not persist captured product events until storage and retention are
  approved.

## Verification

Run the smallest check that covers the change ([docs/CHECKS.md](docs/CHECKS.md)):

```sh
go test ./path/to/changed/package
corepack pnpm lint && corepack pnpm typecheck && corepack pnpm test
```

## Pull requests

Include the outcome, why the implementation is safe, the checks you ran,
screenshots for visible UI changes, and any blockers. Update documentation in
the same change when behaviour or configuration changes.

## License

No repository-wide license is currently declared.
