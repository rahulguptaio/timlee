-- Timlee control plane: identity, tenancy and credentials.
--
-- Tenancy rule: every tenant-scoped row carries `org_id`, and every query in
-- the data-access layer takes an org id. A forgotten filter then fails closed
-- (returns nothing) rather than leaking another tenant's rows.

PRAGMA foreign_keys = ON;

-- ---------------------------------------------------------------- orgs ----

CREATE TABLE IF NOT EXISTS org (
  id          TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  slug        TEXT NOT NULL UNIQUE,
  created_at  INTEGER NOT NULL
);

-- --------------------------------------------------------------- users ----

CREATE TABLE IF NOT EXISTS app_user (
  id             TEXT PRIMARY KEY,
  email          TEXT NOT NULL UNIQUE,
  -- Providers disagree about whether an address is verified. We only ever
  -- link identities automatically when the provider asserts verification.
  email_verified INTEGER NOT NULL DEFAULT 0 CHECK (email_verified IN (0, 1)),
  name           TEXT,
  avatar_url     TEXT,
  created_at     INTEGER NOT NULL,
  last_seen_at   INTEGER,
  disabled_at    INTEGER
);

-- A single user may sign in through several providers.
CREATE TABLE IF NOT EXISTS identity (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  provider      TEXT NOT NULL CHECK (provider IN ('google','github','microsoft','email','saml')),
  -- The provider's immutable subject identifier. Never the email: emails get
  -- reassigned, subjects do not.
  subject       TEXT NOT NULL,
  email         TEXT,
  created_at    INTEGER NOT NULL,
  last_used_at  INTEGER,
  UNIQUE (provider, subject)
);

CREATE INDEX IF NOT EXISTS identity_user_idx ON identity(user_id);

-- --------------------------------------------------------- memberships ----

CREATE TABLE IF NOT EXISTS membership (
  org_id     TEXT NOT NULL REFERENCES org(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  role       TEXT NOT NULL CHECK (role IN ('owner','admin','member','viewer')),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (org_id, user_id)
);

-- Serves "which orgs does this user belong to", the lookup on every request.
CREATE INDEX IF NOT EXISTS membership_user_idx ON membership(user_id);

-- ----------------------------------------------------------- api keys ----

CREATE TABLE IF NOT EXISTS api_key (
  id          TEXT PRIMARY KEY,
  org_id      TEXT NOT NULL REFERENCES org(id) ON DELETE CASCADE,
  created_by  TEXT REFERENCES app_user(id) ON DELETE SET NULL,
  label       TEXT NOT NULL,
  -- The secret is never stored. `prefix` is the visible fragment shown in the
  -- UI so a key can be identified; `hash` is SHA-256 of the full secret.
  prefix      TEXT NOT NULL,
  hash        TEXT NOT NULL UNIQUE,
  scopes      TEXT NOT NULL DEFAULT 'inference:read,inference:write',
  created_at  INTEGER NOT NULL,
  last_used_at INTEGER,
  expires_at  INTEGER,
  revoked_at  INTEGER
);

CREATE INDEX IF NOT EXISTS api_key_org_idx ON api_key(org_id);
-- Authentication looks keys up by hash on every inference request.
CREATE INDEX IF NOT EXISTS api_key_hash_idx ON api_key(hash);

-- ------------------------------------------------------ magic links ------

CREATE TABLE IF NOT EXISTS login_token (
  id         TEXT PRIMARY KEY,
  email      TEXT NOT NULL,
  -- Hashed for the same reason API keys are: a leaked database must not grant
  -- sign-in.
  hash       TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  consumed_at INTEGER
);

CREATE INDEX IF NOT EXISTS login_token_email_idx ON login_token(email);

-- ---------------------------------------------------------- auditing ----

CREATE TABLE IF NOT EXISTS audit_event (
  id         TEXT PRIMARY KEY,
  org_id     TEXT REFERENCES org(id) ON DELETE CASCADE,
  actor_id   TEXT REFERENCES app_user(id) ON DELETE SET NULL,
  action     TEXT NOT NULL,
  target     TEXT,
  metadata   TEXT,
  ip         TEXT,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS audit_org_time_idx ON audit_event(org_id, created_at DESC);
