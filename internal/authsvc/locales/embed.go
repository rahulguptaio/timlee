// Package locales carries the control plane's email catalogs.
package locales

import (
	"embed"

	"github.com/rahulguptaio/timlee/internal/i18n"
)

//go:embed */*.arb
var files embed.FS

func FS() embed.FS { return files }

const (
	MagicSubject i18n.Key = "email.magic.subject"
	MagicHeading i18n.Key = "email.magic.heading"
	MagicBody    i18n.Key = "email.magic.body"
	MagicAction  i18n.Key = "email.magic.action"
	MagicIgnore  i18n.Key = "email.magic.ignore"

	LinkPaste i18n.Key = "email.link.paste"
	LinkValue i18n.Key = "email.link.value"

	DurationMinutes i18n.Key = "email.duration.minutes"
	DurationDays    i18n.Key = "email.duration.days"

	InviteSubject i18n.Key = "email.invite.subject"
	InviteHeading i18n.Key = "email.invite.heading"
	InviteBody    i18n.Key = "email.invite.body"
	InviteAction  i18n.Key = "email.invite.action"
	InviteExpiry  i18n.Key = "email.invite.expiry"
	InviteIgnore  i18n.Key = "email.invite.ignore"
)
