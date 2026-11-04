package authsvc

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	mailocales "github.com/rahulguptaio/timlee/internal/authsvc/locales"
	"github.com/rahulguptaio/timlee/internal/i18n"
)

// Outgoing mail.
//
// No mail provider is wired up, so messages are written to an outbox
// directory as one JSON file each. The local stack runner tails the directory
// and the end-to-end smoke test reads the link out of it, which lets a
// prod-like environment exercise real magic-link sign-in without an inbox.
// With no outbox configured, email sign-in and invitations are both refused
// rather than pretending to send. That is always the case in production,
// where config leaves the outbox unset and Validate refuses one: it writes
// live credentials to disk.

// outboxKeep is enough history to debug a session, and small enough that the
// directory never grows without bound.
const outboxKeep = 200

// outboxFile matches finished messages only; an in-flight write is
// dot-prefixed and ends in .tmp.
var outboxFile = regexp.MustCompile(`^\d+-[\w-]+\.json$`)

// message is the outbox file format. Other tooling parses these files, so the
// field names are a contract.
type message struct {
	ID        string   `json:"id"`
	To        string   `json:"to"`
	From      string   `json:"from"`
	Subject   string   `json:"subject"`
	Text      string   `json:"text"`
	HTML      string   `json:"html"`
	Links     []string `json:"links"`
	CreatedAt string   `json:"createdAt"`
}

type mailer struct {
	outbox string
	from   string
}

func (m mailer) available() bool { return m.outbox != "" }

// send writes one message atomically: a temp file and a rename, so a reader
// tailing the directory never sees half a message. 0600 because the file
// holds a live sign-in link.
func (m mailer) send(msg message) (string, error) {
	if !m.available() {
		return "", fmt.Errorf("no mail transport is configured")
	}
	if err := os.MkdirAll(m.outbox, 0o700); err != nil {
		return "", err
	}

	msg.ID = "msg_" + randomHex(8)
	msg.From = m.from
	msg.CreatedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if msg.Links == nil {
		msg.Links = []string{}
	}

	// Encoded rather than marshalled so HTML escaping stays off: these files
	// are read by people as well as by tooling, and \u003c in place of < in
	// the rendered mail helps nobody.
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(msg); err != nil {
		return "", err
	}
	body := buffer.Bytes()

	name := fmt.Sprintf("%d-%s.json", time.Now().UnixMilli(), msg.ID)
	temp := filepath.Join(m.outbox, "."+name+".tmp")
	if err := os.WriteFile(temp, body, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(temp, filepath.Join(m.outbox, name)); err != nil {
		os.Remove(temp)
		return "", err
	}

	m.prune()
	return name, nil
}

// prune keeps the newest outboxKeep messages. Names start with a millisecond
// timestamp, so name order is age order.
func (m mailer) prune() {
	entries, err := os.ReadDir(m.outbox)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if outboxFile.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= outboxKeep {
		return
	}
	sort.Slice(names, func(i, j int) bool {
		left, right := timestampOf(names[i]), timestampOf(names[j])
		if left != right {
			return left < right
		}
		return names[i] < names[j]
	})
	for _, name := range names[:len(names)-outboxKeep] {
		os.Remove(filepath.Join(m.outbox, name))
	}
}

func timestampOf(name string) int64 {
	prefix, _, _ := strings.Cut(name, "-")
	value, _ := strconv.ParseInt(prefix, 10, 64)
	return value
}

// magicLinkMessage renders the sign-in email in the recipient's locale.
func magicLinkMessage(l *i18n.Localizer, to, link string, now, expiresAt time.Time) message {
	duration := emailDuration(l, expiresAt.Sub(now))
	subject := l.T(mailocales.MagicSubject)
	bodyText := l.T(
		mailocales.MagicBody,
		"email", to,
		"expiresAt", i18n.Formatted(l.DateTime(expiresAt, i18n.DateMedium)),
		"duration", duration,
	)
	text := strings.Join([]string{
		l.T(mailocales.MagicHeading),
		"",
		bodyText,
		"",
		l.T(mailocales.LinkValue, "url", link),
		"",
		l.T(mailocales.MagicIgnore),
		"",
	}, "\n")

	loc := l.Locale()
	safeLink := html.EscapeString(link)
	body := `<!doctype html>
<html lang="` + html.EscapeString(loc.Lang) + `" dir="` + html.EscapeString(loc.Dir) + `">
<head><meta charset="utf-8"><title>` + html.EscapeString(subject) + `</title></head>
<body style="font-family: system-ui, sans-serif; line-height: 1.5; color: #111;">
  <h1 style="font-size: 20px;">` + string(l.HTML(mailocales.MagicHeading)) + `</h1>
  <p>` + string(l.HTML(
		mailocales.MagicBody,
		"email", to,
		"expiresAt", i18n.Formatted(l.DateTime(expiresAt, i18n.DateMedium)),
		"duration", duration,
	)) + `</p>
  <p><a href="` + safeLink + `" style="display: inline-block; padding: 10px 16px; background: #111; color: #fff; text-decoration: none; border-radius: 6px;">` + string(l.HTML(mailocales.MagicAction)) + `</a></p>
  <p style="font-size: 13px; color: #555;">` + string(l.HTML(mailocales.LinkPaste)) + `<br>` + string(l.HTML(mailocales.LinkValue, "url", link)) + `</p>
  <p style="font-size: 13px; color: #555;">` + string(l.HTML(mailocales.MagicIgnore)) + `</p>
</body>
</html>
`

	return message{To: to, Subject: subject, Text: text, HTML: body, Links: []string{link}}
}

// invitationMessage renders the organisation invitation email. Every
// interpolated value comes from user input — an org name, an inviter's
// address — so every one of them is escaped in the HTML part.
func invitationMessage(l *i18n.Localizer, to, orgName, inviter, link string, now, expiresAt time.Time) message {
	subject := l.T(mailocales.InviteSubject, "org", orgName)
	hasInviter := "other"
	if inviter != "" {
		hasInviter = "yes"
	}
	bodyArgs := []any{"hasInviter", hasInviter, "inviter", inviter, "org", orgName, "email", to}
	expiryArgs := []any{
		"expiresAt", i18n.Formatted(l.DateTime(expiresAt, i18n.DateMedium)),
		"duration", emailDuration(l, expiresAt.Sub(now)),
	}

	text := strings.Join([]string{
		subject,
		"",
		l.T(mailocales.InviteBody, bodyArgs...),
		"",
		l.T(mailocales.LinkValue, "url", link),
		"",
		l.T(mailocales.InviteExpiry, expiryArgs...),
		"",
		l.T(mailocales.InviteIgnore),
		"",
	}, "\n")

	loc := l.Locale()
	safeLink := html.EscapeString(link)
	body := `<!doctype html>
<html lang="` + html.EscapeString(loc.Lang) + `" dir="` + html.EscapeString(loc.Dir) + `">
<head><meta charset="utf-8"><title>` + html.EscapeString(subject) + `</title></head>
<body style="font-family: system-ui, sans-serif; line-height: 1.5; color: #111;">
  <h1 style="font-size: 20px;">` + string(l.HTML(mailocales.InviteHeading, "org", orgName)) + `</h1>
  <p>` + string(l.HTML(mailocales.InviteBody, bodyArgs...)) + `</p>
  <p><a href="` + safeLink + `" style="display: inline-block; padding: 10px 16px; background: #111; color: #fff; text-decoration: none; border-radius: 6px;">` + string(l.HTML(mailocales.InviteAction)) + `</a></p>
  <p style="font-size: 13px; color: #555;">` + string(l.HTML(mailocales.LinkPaste)) + `<br>` + string(l.HTML(mailocales.LinkValue, "url", link)) + `</p>
  <p style="font-size: 13px; color: #555;">` + string(l.HTML(mailocales.InviteExpiry, expiryArgs...)) + `</p>
  <p style="font-size: 13px; color: #555;">` + string(l.HTML(mailocales.InviteIgnore)) + `</p>
</body>
</html>
`

	return message{To: to, Subject: subject, Text: text, HTML: body, Links: []string{link}}
}

func emailDuration(l *i18n.Localizer, duration time.Duration) i18n.Formatted {
	if duration%time.Hour == 0 && duration >= 24*time.Hour {
		return i18n.Formatted(l.T(mailocales.DurationDays, "count", int64(duration/(24*time.Hour))))
	}
	return i18n.Formatted(l.T(mailocales.DurationMinutes, "count", int64(duration/time.Minute)))
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("authsvc: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}
