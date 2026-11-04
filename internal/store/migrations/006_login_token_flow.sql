-- Which app window a desktop sign-in code belongs to.
--
-- A desktop code is bound to the app's PKCE challenge, so a stolen code is
-- useless without the verifier. That does not stop login CSRF: an attacker
-- redeeming a code for their own account, with their own verifier, in someone
-- else's browser. flow_hash binds the code to the random flow id the site set
-- as a cookie in the app's own window when the sign-in started, so the code is
-- redeemed only in that window. Only a hash is stored, like every credential
-- here.
ALTER TABLE login_token ADD COLUMN flow_hash TEXT;
