-- Where an email sign-in link lands, so it returns the user to the page that
-- asked them to sign in, like the OAuth flow does. Always an app-origin URL
-- (validated on write and again on use).
ALTER TABLE login_token ADD COLUMN return_to TEXT;
