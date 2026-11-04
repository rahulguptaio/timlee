-- The user's saved language, and the language carried by a one-shot sign-in
-- link until it can become that preference on first sign-in.
ALTER TABLE app_user ADD COLUMN locale TEXT;
ALTER TABLE login_token ADD COLUMN locale TEXT;
