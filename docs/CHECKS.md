# Checks

Run the smallest check that covers a change; CI (`.github/workflows/ci.yml`)
runs all of them.

| Area | Command |
|---|---|
| Go build | `go build ./...` |
| Go vet | `go vet ./...` |
| Go tests | `go test ./...` (add `-race` for concurrency changes) |
| Frontend lint | `corepack pnpm lint` (Biome) |
| TypeScript | `corepack pnpm typecheck` |
| Components | `corepack pnpm test` (Vitest browser mode, Chromium) |
| Asset build | `go run ./cmd/timlee-build` |
| Desktop | `cargo test --manifest-path desktop/src-tauri/Cargo.toml` |

Git hooks (`lefthook.yml`) run gofmt, `go vet` and `go test -short` on the
packages a commit touches, Biome on staged frontend files, and a Conventional
Commits check on the message:

```sh
go tool -modfile=tools/hooks/go.mod lefthook install
```
