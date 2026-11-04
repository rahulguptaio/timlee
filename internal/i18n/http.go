package i18n

import (
	"context"

	"golang.org/x/text/language"
)

type contextKey struct{}

type requestLocale struct {
	loc     *Locale
	fromURL bool
}

// WithLocale returns ctx carrying a request's locale. fromURL records that
// the URL named it, as a prefix of a public page, rather than a preference
// or the default supplying it.
func WithLocale(ctx context.Context, loc *Locale, fromURL bool) context.Context {
	return context.WithValue(ctx, contextKey{}, requestLocale{loc: loc, fromURL: fromURL})
}

// FromContext is the locale WithLocale stored, or nil.
func FromContext(ctx context.Context) (loc *Locale, fromURL bool) {
	v, _ := ctx.Value(contextKey{}).(requestLocale)
	return v.loc, v.fromURL
}

// Negotiate picks the shipped locale an Accept-Language header asks for, and
// how sure the match is. Pseudo-locales are never chosen. It is for
// suggesting a language, never for choosing what a URL serves.
func (b *Bundle) Negotiate(acceptLanguage string) (*Locale, language.Confidence) {
	desired, _, err := language.ParseAcceptLanguage(acceptLanguage)
	if err != nil || len(desired) == 0 {
		return b.Default(), language.No
	}
	// The index, not the returned tag, which may carry -u-rg extensions.
	_, i, confidence := b.matcher.Match(desired...)
	return b.matchable[i], confidence
}
