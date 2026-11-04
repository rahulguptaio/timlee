package main

import (
	"strings"

	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/internal/render"
)

// navLinks are the header's links, the same on every page. The signed-in and
// signed-out calls to action are decided by the nav template itself.
func navLinks(*i18n.Localizer) []render.Link {
	return nil
}

// footerGroups are the site's real destinations.
func footerGroups(l *i18n.Localizer) []render.LinkGroup {
	return []render.LinkGroup{
		{Title: l.T("common.footer.product"), Links: []render.Link{
			{Label: l.T("common.footer.apps"), Href: "/apps"},
			{Label: l.T("common.nav.settings"), Href: "/settings"},
		}},
	}
}

func site(l *i18n.Localizer) render.Site {
	return render.Site{
		Nav:     navLinks(l),
		Footer:  footerGroups(l),
		Tagline: l.T("common.footer.tagline"),
		Legal:   l.T("common.footer.copyright"),
	}
}

// trimSlash normalises an origin for display and for joining.
func trimSlash(origin string) string { return strings.TrimSuffix(origin, "/") }
