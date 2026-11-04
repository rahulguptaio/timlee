package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

// Prefixed, URL-safe ids make logs and support tickets readable, and make it
// obvious when the wrong kind of id has been passed somewhere.

// token returns n random bytes, base64url encoded.
func token(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on any supported platform; if it ever
		// did, minting a guessable secret would be far worse than a panic.
		panic("store: crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func newOrgID() string      { return "org_" + token(12) }
func newUserID() string     { return "usr_" + token(12) }
func newIdentityID() string { return "idn_" + token(12) }
func newEventID() string    { return "evt_" + token(12) }
func newKeyID() string      { return "key_" + token(12) }

// NewSessionID mints the opaque identifier a session cookie carries.
func NewSessionID() string { return token(24) }

// NewLoginToken mints a single-use sign-in token for the magic-link flow,
// returning the secret to mail out and the hash to persist.
func NewLoginToken() (secret, hash string) {
	secret = token(32)
	return secret, HashSecret(secret)
}

// NewAPIKey mints a key for the data plane.
//
// The full secret is returned exactly once. Only the hash and a short visible
// prefix are persisted, so a database leak cannot be replayed against the
// inference endpoint.
func NewAPIKey() (secret, prefix, hash string) {
	secret = "mk_live_" + token(24)
	// Enough to recognise a key in a list, far too little to use.
	return secret, secret[:12], HashSecret(secret)
}

// HashSecret is SHA-256 of a secret, hex encoded. A fast hash is correct
// here: the input is 128+ bits of entropy, so there is nothing to brute force
// and nothing a work factor would buy.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// PKCEChallenge derives the S256 code challenge for a verifier, as RFC 7636
// defines it: base64url of the SHA-256, without padding.
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// PKCEMatches reports whether a verifier produces a challenge, in constant
// time. An empty verifier never matches: a bound credential must be redeemed
// with the proof it was issued against, not with the absence of one.
func PKCEMatches(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	return SecretsMatch(PKCEChallenge(verifier), challenge)
}

// SecretsMatch compares two secrets without leaking their contents through
// timing. A plain == returns as soon as it finds a differing byte, so how
// long it takes reveals how much of a guess was correct.
func SecretsMatch(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
