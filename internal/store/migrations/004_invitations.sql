-- Organisation invitations.
--
-- An invitation is a credential: whoever holds the token can join the
-- organisation it names. So, like api_key and login_token, only a SHA-256 of
-- the token is stored — a copy of this file must not be replayable as an
-- invitation.
--
-- `role` is the membership the invitation grants. It is written at invite
-- time and checked again on accept, so an invitation can never grant more
-- than the inviter was allowed to give.

CREATE TABLE IF NOT EXISTS invitation (
  id          TEXT PRIMARY KEY,
  org_id      TEXT NOT NULL REFERENCES org(id) ON DELETE CASCADE,
  -- Lower-cased at the point of writing, so the match on accept is exact.
  email       TEXT NOT NULL,
  role        TEXT NOT NULL CHECK (role IN ('owner','admin','member','viewer')),
  hash        TEXT NOT NULL UNIQUE,
  invited_by  TEXT REFERENCES app_user(id) ON DELETE SET NULL,
  created_at  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL,
  accepted_at INTEGER,
  accepted_by TEXT REFERENCES app_user(id) ON DELETE SET NULL,
  revoked_at  INTEGER
);

-- One live invitation per address per organisation: re-inviting someone
-- rotates their token in place instead of leaving two usable ones behind.
-- The index is partial so a spent or revoked invitation never blocks a new
-- one for the same address.
CREATE UNIQUE INDEX IF NOT EXISTS invitation_live_idx
  ON invitation(org_id, email)
  WHERE accepted_at IS NULL AND revoked_at IS NULL;

-- Serves the admin console's pending list.
CREATE INDEX IF NOT EXISTS invitation_org_idx ON invitation(org_id, created_at DESC);
