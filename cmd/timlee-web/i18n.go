package main

import (
	"net/http"
	"sync"

	"github.com/rahulguptaio/timlee/internal/i18n"
	"github.com/rahulguptaio/timlee/web/locales"
)

// englishOnly is the bundle of a server built without one, as the tests
// build it: the default locale, from the embedded catalogs.
var englishOnly = sync.OnceValues(func() (*i18n.Bundle, error) {
	return i18n.Load(locales.FS(), nil, false)
})

// bundle is the locales this server serves.
func (s *server) bundle() *i18n.Bundle {
	if s.i18n != nil {
		return s.i18n
	}
	b, err := englishOnly()
	if err != nil {
		// main loads the same catalogs before listening and exits on
		// this error, so only a test can get here with broken catalogs.
		panic("timlee-web: the embedded catalogs do not load: " + err.Error())
	}
	return b
}

// localizer is the request's: the locale its URL names, the reader's on an
// app page (pageLocale), otherwise the default.
func (s *server) localizer(r *http.Request) *i18n.Localizer {
	return s.pageLocale(r, nil).Localizer()
}
