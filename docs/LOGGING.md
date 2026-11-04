# Logging

Timlee's services log on two channels, built by [`internal/logx`](../internal/logx):

| Channel | What it is for | Where it goes today |
| --- | --- | --- |
| Operational log | What an operator reads: startup, shutdown, one line per request, warnings, errors | JSON lines on stdout |
| Event channel | What happened for the product: what users did, which requests failed | **Nowhere.** Events are captured and discarded |

The event channel is deliberately not written anywhere yet. Until Timlee has storage for events, its sink discards every one of them: nothing reaches stdout, stderr, a file, a database or the network, and no setting or environment variable changes that. Once storage exists, one line at startup plugs it in (see [Adding a sink](#adding-a-sink)).

## Operational log

Every service builds its logger with `logx.New`, and writes the same lines it always has: one JSON object per line on stdout, with `time`, `level` and `msg`, plus `service` on every line of `timlee-web` (`timlee-auth`'s lines do not carry it).

`timlee-auth` and `timlee-web` write through `httpx.LogWriter`: info lines are batched and reach stdout within about 100 ms, or when the process shuts down cleanly; a warning or an error is written at once, with everything batched before it. So a line missing from a running service's output may only be waiting in the buffer: a test that asserts a line is absent reads the output after the service has exited cleanly (its last line is `stopped`) or after closing the writer.

The request line, written by `httpx.Logging` after each response:

| Field | Meaning |
| --- | --- |
| `method`, `path` | The request's method and URL path. Never the query string, headers, cookies or body |
| `status`, `bytes` | What was sent back |
| `duration_ms` | Time to the end of the response |
| `request_id` | `X-Request-Id`: the caller's, or a 32-digit hex id minted here and echoed back. `timlee-web` forwards it to the control plane, so one reader's action has one id in both services' logs |

The operational log carries no user, organisation or key ids, and never prompt text.

| Variable | Effect |
| --- | --- |
| `TIMLEE_LOG_LEVEL` | `debug`, `info`, `warn` or `error`; overrides the service's own default (info in production, debug elsewhere) |
| `TIMLEE_LOG_FORMAT` | `json` (default) or `text`, slog's `key=value` format, for a person at a terminal. Keep JSON wherever a program reads the log: the load harness parses it line by line |
| `LOG_LEVEL` | The older setting `timlee-auth` reads; `TIMLEE_LOG_LEVEL` wins when both are set |

An unusable value is ignored, with one `logging configuration ignored` warning saying why.

## Event channel

`logx.Event(ctx, name, attrs...)` records that something happened. A sink receives a `logx.Record`: the time, the event's name, and its attributes in this order:

1. the process: `service`, `version`, `commit`, `env`, `instance` (host name);
2. the request the event belongs to, when there is one: `request_id`, `trace_id`, `user_id`, `org_id`, `key_id`;
3. the event's own attributes, which win over the request's when they share a name.

Every attribute has already been redacted (see [What is never logged](#what-is-never-logged)). `Record.MarshalJSON` renders a record as one JSON object with the name under `event`, the line a file or table would store.

### Requests

`httpx.Logging` is each request's boundary. As a request arrives it opens a scope that events emitted while serving it read their request fields from; when the response is written it closes the scope and, if the request failed, emits `request.failed`, once. While events are discarded neither step does anything but check that they are: no allocation, a few nanoseconds (`BenchmarkRequestScope`).

| Field | Source |
| --- | --- |
| `request_id` | `X-Request-Id`, as on the request line, cut to 128 characters |
| `trace_id` | The trace id of a valid W3C `traceparent` header; without one, the request id when it is 32 hex digits (as the ones Timlee mints are), else the first 32 hex digits of its SHA-256. The same request id gives the same trace id in every service |
| `user_id`, `org_id`, `key_id` | Set with `logx.Identify` where the service authenticates the caller: the control plane's `authed` and `scoped` (`internal/authsvc/account.go`) |

A handler that turns an error into a response calls `logx.Fail(r, err)` instead of emitting anything itself: the request's `request.failed` then carries the cause and the route the handler matched. `httpx.Recover` does the same for a panic, with its stack.

### Catalogue

Names are stable: a sink and anything reading it may key on them. Rename one only with a migration of whatever stores it.

| Event | Emitted by | When |
| --- | --- | --- |
| `request.failed` | `httpx.Logging`, every service with it in its chain | A response with status 500 or above, or a request a handler marked with `logx.Fail` or that panicked |
| `audit.api_key.created` | `internal/authsvc/account.go` | An API key is created |
| `audit.api_key.revoked` | `internal/authsvc/account.go` | An API key is revoked |
| `audit.auth.sign_in_failed` | `internal/authsvc/auth_routes.go` | An OAuth callback or a sign-in link fails |
| `audit.session.revoked` | `internal/authsvc/auth_routes.go` | A signed-in user signs out and the server deletes their session |
| `audit.org.created` | `internal/authsvc/auth_routes.go` | A first sign-in creates the user's workspace |
| `audit.settings.changed` | `internal/authsvc/account.go` | A user changes their profile (`PATCH /api/v1/me`) |

The `audit.*` events sit beside the audit trail (the `audit_event` table), not in place of it: they add no rows. The store already writes `api_key.created`, `api_key.revoked` and `org.created` rows itself, and sign-out writes `auth.signed_out`. Failed sign-ins (other than a refused account link) and profile changes have no row.

#### `request.failed`

| Field | Meaning |
| --- | --- |
| `method` | The request's method |
| `route` | The `ServeMux` pattern the request matched, such as `POST /settings/keys`: bounded, and free of ids and tokens in paths. `unknown` when neither the boundary nor a handler saw the pattern (a middleware that wraps the request before the mux hides it from the boundary) |
| `status` | The status sent. 200 is possible: a streamed reply whose upstream failed after the first byte |
| `duration_ms` | Time to the end of the response |
| `error` | The cause a handler recorded: `message` (redacted, at most 512 bytes) and `chain`, the Go type of each error it wraps, outermost first. An upstream failure is named by its status and code only (`upstream answered 503 overloaded`): the upstream's own message may quote the prompt |
| `panic`, `stack` | A recovered panic, and the stack of the goroutine that panicked (at most 8 KiB) |

#### `audit.*`

Each carries the ids the request was identified with (an API route behind `authed` or `scoped` has `user_id`, and `org_id` too when scoped), besides:

| Event | Fields |
| --- | --- |
| `audit.api_key.created` | `key_id` (the key created), `label` as typed, redacted. Never the key or its prefix |
| `audit.api_key.revoked` | `key_id` |
| `audit.auth.sign_in_failed` | `provider` (`github`, `google`, `microsoft`, `email`, or empty when unknown) and `reason`: `no_flow`, `state_mismatch`, `provider_denied`, `exchange_failed`, `identity_conflict`, `link_invalid`, `pkce_failed` or `unknown`. Never the email address or the provider's own error text, which a query parameter can carry |
| `audit.session.revoked` | `user_id` |
| `audit.org.created` | `user_id`, `org_id` (the workspace created) |
| `audit.settings.changed` | `settings`: the names of the settings changed, comma-separated (today only `name`). Never their values |

### Adding an event

1. Name it `<area>.<what happened>` in the past tense or as a result (`request.failed`, `audit.api_key.revoked`) and add it to the catalogue above.
2. Give it counts, codes, durations and opaque ids. Never text a person wrote, a name, an address or a secret.
3. If describing it costs work, check `logx.Enabled()` first, so a discarded event costs nothing.
4. Test it with `logx.Capture(t)`, which keeps events in memory for the test.

### Adding a sink

A sink implements `logx.Sink`:

```go
type Sink interface {
    Write(logx.Record)
}
```

`Write` runs on the request path, so a sink that does I/O must queue and write in the background, and it must be safe for concurrent use. Plugging one in is one line in each service's `main`, after `logx.New`, for example with a sink backed by the store:

```go
logx.SetSink(eventSink)
```

Before doing that, settle retention and deletion for events (below).

## What is never logged

On either channel:

- **Prompt text.** Events carry counts; nothing in `logx` accepts or formats message text.
- **Secrets**: API keys, session ids and cookies, sign-in link tokens, OAuth codes and state, the internal token, CSRF tokens. The operational log never had them; events are redacted before any sink sees them:
  - an attribute whose name has a segment `authorization`, `cookie`, `password`, `secret`, `token`, `csrf`, `apikey`, `credential`, `session` or `sid`, or `api_key`/`private_key`, is replaced with `[redacted]` whatever it holds (`csrf_token` and `Set-Cookie` are, `tokens_saved` is not);
  - in any other text, anything shaped like a Timlee API key (`mk_<env>_…`), a bearer token, a JWT, or a credential in a URL query (`?code=`, `&state=`, `&token=`…) becomes `[redacted]`;
  - errors are reduced to their redacted message and the types in their chain; any other non-scalar value to its redacted text.
- **Email addresses.** Masked wherever they appear in an event: `alice@example.com` becomes `a***@example.com`.
- **IP addresses.** Events carry none. The operational log records an address only on a rate-limited request, as before.

A test plants a canary in every one of these shapes and fails if it survives (`TestRedactionNeverLeaksTheCanary` in `internal/logx`); another runs the event channel's whole surface in a child process and fails if anything reaches its stdout or stderr (`TestEventsAreDiscardedByDefault`).

## Identifiers

People and things appear only as the opaque ids the store mints: `usr_…`, `org_…`, `key_…`, `idn_…`. They mean nothing outside the database, and resolving one needs access to it. `request_id` and `trace_id` identify requests, not people.

## Retention

To be decided when event storage exists. Nothing is kept today: events are discarded as they are emitted. The operational log's retention is whatever collects stdout.

### Audit rows and account deletion

There is no way to delete an account today: nothing in `internal/store` or `internal/authsvc` deletes a user or an organisation. The schema decides what would happen (SQLite enforces foreign keys; `store.Open` turns them on):

- Deleting a user keeps their audit rows but clears `actor_id` (`ON DELETE SET NULL`). What else the rows hold stays: `target` (a user id on `member.*` rows), `metadata` (the invitee's address on `member.invited`, the provider on `auth.signed_in`) and `ip`.
- Deleting an organisation deletes its audit rows (`ON DELETE CASCADE`). Account-level rows (`auth.signed_in`, `auth.signed_out`, `identity.unlinked`, `auth.link_refused`) have no organisation and survive it.

So a deletion feature has to scrub or delete a user's rows itself. Events, once stored, will need the same treatment for their `user_id`.
