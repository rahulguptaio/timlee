-- Sessions by the account they belong to, for "sign out of other sessions".
--
-- A session's owner is inside its JSON document. An index on the expression
-- the store queries by finds one account's sessions without reading every
-- row; json_extract is deterministic, so SQLite can index it.
CREATE INDEX IF NOT EXISTS session_user_idx ON session(json_extract(sess, '$.userId'));
