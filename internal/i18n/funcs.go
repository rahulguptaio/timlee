package i18n

import (
	"errors"
	"fmt"
	"html/template"
	"time"
)

// FuncMap is the template functions that localize. The localizer is always
// the first argument, `{{ t .L "common.nav.signIn" }}`, which keeps the map
// static (templates are still parsed once) and every call greppable.
// Arguments after the key are name/value pairs, like dict.
//
// The formatters return Formatted, so a value formatted in a template and
// passed into a message is printed as it is rather than isolated.
func FuncMap() template.FuncMap {
	return template.FuncMap{
		"t":        tmplT,
		"tn":       tmplTN,
		"th":       tmplTH,
		"num":      tmplNum,
		"pct":      tmplPct,
		"compact":  tmplCompact,
		"date":     tmplDate,
		"time":     tmplTime,
		"datetime": tmplDateTime,
		"ago":      tmplAgo,
		"dur":      tmplDur,
		"href":     tmplHref,
		"isolate":  Isolate,
	}
}

// Isolate wraps text of unknown direction in FIRST STRONG ISOLATE … POP
// DIRECTIONAL ISOLATE, for places that cannot hold a <bdi>: attributes,
// titles, plain-text email.
func Isolate(s string) string { return fsi + s + pdi }

var errNoLocalizer = errors.New("i18n: no localizer: the page's L is nil")

// tmplT is `t`: a message as plain text, escaped by html/template wherever
// it lands.
func tmplT(l *Localizer, key string, args ...any) (string, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	s, err := l.text(Key(key), nil, args)
	if err != nil {
		return "", fmt.Errorf("t %q: %w", key, err)
	}
	return s, nil
}

// tmplTN is `tn`: a message whose plural "count" is n.
func tmplTN(l *Localizer, key string, n any, args ...any) (string, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	count, ok := toInt64(n)
	if !ok {
		return "", fmt.Errorf("tn %q: count is %T, want an integer", key, n)
	}
	s, err := l.text(Key(key), &count, args)
	if err != nil {
		return "", fmt.Errorf("tn %q: %w", key, err)
	}
	return s, nil
}

// tmplTH is `th`: a rich message as HTML.
func tmplTH(l *Localizer, key string, args ...any) (template.HTML, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	h, err := l.html(Key(key), nil, args)
	if err != nil {
		return "", fmt.Errorf("th %q: %w", key, err)
	}
	return h, nil
}

// tmplNum is `num`: an integer, or a float to at most three decimals.
func tmplNum(l *Localizer, v any) (Formatted, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	if n, ok := toInt64(v); ok {
		return Formatted(l.Int(n)), nil
	}
	if f, ok := toFloat(v); ok {
		return Formatted(l.Decimal(f, 3)), nil
	}
	return "", fmt.Errorf("num: %T is not a number", v)
}

// tmplPct is `pct`: a ratio as a whole percentage.
func tmplPct(l *Localizer, ratio any) (Formatted, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	f, ok := toFloat(ratio)
	if !ok {
		return "", fmt.Errorf("pct: %T is not a number", ratio)
	}
	return Formatted(l.Percent(f)), nil
}

// tmplCompact is `compact`: an abbreviated count.
func tmplCompact(l *Localizer, v any) (Formatted, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	n, ok := toInt64(v)
	if !ok {
		return "", fmt.Errorf("compact: %T is not an integer", v)
	}
	return Formatted(l.Compact(n)), nil
}

// tmplDate is `date`: DateMedium, or DateMonthDay with "monthDay".
func tmplDate(l *Localizer, t time.Time, style ...string) (Formatted, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	s, err := dateStyle(style)
	if err != nil {
		return "", err
	}
	return Formatted(l.Date(t, s)), nil
}

// tmplTime is `time`: the time of day.
func tmplTime(l *Localizer, t time.Time) (Formatted, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	return Formatted(l.Time(t)), nil
}

// tmplDateTime is `datetime`: a date and a time of day.
func tmplDateTime(l *Localizer, t time.Time, style ...string) (Formatted, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	s, err := dateStyle(style)
	if err != nil {
		return "", err
	}
	return Formatted(l.DateTime(t, s)), nil
}

// tmplAgo is `ago`: t relative to now.
func tmplAgo(l *Localizer, t time.Time) (Formatted, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	return Formatted(l.Relative(t, time.Now())), nil
}

// tmplDur is `dur`: a short duration.
func tmplDur(l *Localizer, d time.Duration) (Formatted, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	return Formatted(l.Duration(d)), nil
}

// tmplHref is `href`: a path on this site where the page's reader finds it,
// "/models" as "/de/models" on a German page (Localizer.Path).
func tmplHref(l *Localizer, path string) (string, error) {
	if l == nil {
		return "", errNoLocalizer
	}
	return l.Path(path), nil
}

func dateStyle(style []string) (DateStyle, error) {
	switch {
	case len(style) == 0 || len(style) == 1 && style[0] == "medium":
		return DateMedium, nil
	case len(style) == 1 && style[0] == "monthDay":
		return DateMonthDay, nil
	}
	return 0, fmt.Errorf("date style %q is not medium or monthDay", style)
}

func toFloat(v any) (float64, bool) {
	switch v := v.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	}
	if n, ok := toInt64(v); ok {
		return float64(n), true
	}
	return 0, false
}
