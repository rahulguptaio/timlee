-- Which key replaced this one, for keys rotated rather than revoked.
--
-- Rotating a key creates its replacement and sets the old key to stop at the
-- end of an overlap window, so both work while the new one is deployed. The
-- link lets the console show the two as a pair, and stops a key from being
-- rotated twice into two live replacements.
ALTER TABLE api_key ADD COLUMN replaced_by TEXT REFERENCES api_key(id) ON DELETE SET NULL;
