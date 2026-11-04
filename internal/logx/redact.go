package logx

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Redaction. Events will be stored, so what they carry is decided here,
// before any sink sees them, rather than trusted to each call site:
//
//   - an attribute whose name says it holds a credential (authorization,
//     cookie, password, secret, token, csrf, api key, session…) is replaced
//     whatever its value looks like;
//   - in any other string, anything shaped like a Timlee API key, a bearer
//     token, a JWT or a credential in a URL query is replaced;
//   - an email address keeps its first letter and its domain.
//
// It is a safety net, not a licence: an event carries counts, codes and
// opaque ids, and nothing a person typed. Prompt and completion text is never
// passed to Event, and no helper here formats it.

const redacted = "[redacted]"

// sensitiveWords mark an attribute name as holding a credential when they
// appear as a whole segment of it: "csrf_token" and "Set-Cookie" match,
// "tokens_saved" does not.
var sensitiveWords = map[string]bool{
	"authorization": true, "cookie": true, "cookies": true, "password": true, "passwd": true,
	"secret": true, "secrets": true, "token": true, "csrf": true, "xsrf": true, "apikey": true,
	"credential": true, "credentials": true, "session": true, "sid": true,
}

func sensitiveKey(key string) bool {
	previous := ""
	for _, word := range strings.FieldsFunc(strings.ToLower(key), func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == ' '
	}) {
		if sensitiveWords[word] || (word == "key" && (previous == "api" || previous == "private")) {
			return true
		}
		previous = word
	}
	return false
}

var (
	// Timlee's keys are mk_<environment>_<base64url>; see store.NewAPIKey.
	apiKeyPattern = regexp.MustCompile(`mk_[a-z]+_[A-Za-z0-9_-]{8,}`)
	bearerPattern = regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/=-]+`)
	jwtPattern    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]{4,}\.[A-Za-z0-9_-]*`)
	queryPattern  = regexp.MustCompile(`(?i)([?&](?:access_token|id_token|refresh_token|token|code|code_verifier|state|key|api_key|secret|password|sig|signature)=)[^&#\s"']+`)
	emailPattern  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
)

// redactString removes credentials and masks email addresses in s. Each
// pattern runs only when a cheap test says it could match.
func redactString(s string) string {
	if strings.Contains(s, "mk_") {
		s = apiKeyPattern.ReplaceAllString(s, redacted)
	}
	if strings.Contains(strings.ToLower(s), "bearer") {
		s = bearerPattern.ReplaceAllString(s, "${1} "+redacted)
	}
	if strings.Contains(s, "eyJ") {
		s = jwtPattern.ReplaceAllString(s, redacted)
	}
	if strings.Contains(s, "=") && strings.ContainsAny(s, "?&") {
		s = queryPattern.ReplaceAllString(s, "${1}"+redacted)
	}
	if strings.Contains(s, "@") {
		s = emailPattern.ReplaceAllStringFunc(s, maskEmail)
	}
	return s
}

// maskEmail keeps the first character and the domain: enough to tell two
// addresses apart while reading, not enough to write to either.
func maskEmail(address string) string {
	at := strings.LastIndexByte(address, '@')
	if at < 1 {
		return redacted
	}
	return address[:1] + "***" + address[at:]
}

// redact applies the rules above to one attribute, groups included. A value
// of any other type is reduced to its text first, so that no sink is handed a
// structure these rules could not see into.
func redact(attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	if sensitiveKey(attr.Key) {
		return slog.String(attr.Key, redacted)
	}
	switch attr.Value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(redactString(attr.Value.String()))
	case slog.KindGroup:
		group := attr.Value.Group()
		out := make([]slog.Attr, 0, len(group))
		for _, member := range group {
			out = append(out, redact(member))
		}
		attr.Value = slog.GroupValue(out...)
	case slog.KindAny:
		switch value := attr.Value.Any().(type) {
		case error:
			attr = errAttr(attr.Key, value)
		case []string:
			out := make([]string, len(value))
			for i, s := range value {
				out[i] = redactString(s)
			}
			attr.Value = slog.AnyValue(out)
		default:
			attr.Value = slog.StringValue(redactString(fmt.Sprint(value)))
		}
	}
	return attr
}

// maxMessage bounds an error message an event carries; maxChain bounds the
// errors listed from one chain.
const (
	maxMessage = 512
	maxChain   = 16
)

// Err describes err for an event: its message, redacted, and the type of
// every error in its chain, outermost first, following both Unwrap() error
// and Unwrap() []error.
func Err(err error) slog.Attr { return errAttr("error", err) }

func errAttr(key string, err error) slog.Attr {
	if err == nil {
		return slog.Attr{}
	}
	return slog.Group(key,
		slog.String("message", truncate(redactString(err.Error()), maxMessage)),
		slog.Any("chain", chainOf(err)),
	)
}

func chainOf(err error) []string {
	var chain []string
	var walk func(error)
	walk = func(err error) {
		for err != nil && len(chain) < maxChain {
			chain = append(chain, fmt.Sprintf("%T", err))
			switch wrapped := err.(type) { //nolint:errorlint // walking the chain by hand is the point
			case interface{ Unwrap() []error }:
				for _, inner := range wrapped.Unwrap() {
					walk(inner)
				}
				return
			case interface{ Unwrap() error }:
				err = wrapped.Unwrap()
			default:
				return
			}
		}
	}
	walk(err)
	return chain
}

// truncate cuts s to at most n bytes, on a character boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
