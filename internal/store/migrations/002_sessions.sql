-- Browser sessions for express-session (src/lib/session-store.js).
--
-- The in-memory store loses every session on restart and grows without bound,
-- so sessions live next to the data they refer to.
--
-- `sid` holds SHA-256 of the session id, not the id itself, so a copy of this
-- file cannot be replayed as a cookie. `sess` is the JSON session document.
-- `expires_at` (epoch ms) is authoritative; expired rows are invisible to
-- reads and pruned periodically.

CREATE TABLE IF NOT EXISTS session (
  sid        TEXT PRIMARY KEY,
  sess       TEXT NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS session_expires_idx ON session(expires_at);
