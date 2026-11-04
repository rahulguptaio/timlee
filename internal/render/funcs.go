package render

import (
	"fmt"
	"html/template"
	"maps"
	"strconv"
	"strings"

	"github.com/rahulguptaio/timlee/internal/i18n"
)

// fillStep is the granularity of the proportional-width classes below.
//
// A bar's width is data, not decoration, yet a Content-Security-Policy
// without 'unsafe-inline' forbids the style attribute that would normally
// carry it. Shipping the discrete steps the page actually uses as classes
// keeps the policy strict and the bars honest; 5% is finer than the eye
// reads off an unlabelled bar and costs twenty-one rules.
const fillStep = 5

// FillCSS is the stylesheet for those classes, emitted inline under the
// response nonce.
var FillCSS = buildFillCSS()

func buildFillCSS() template.CSS {
	var b strings.Builder
	for pct := 0; pct <= 100; pct += fillStep {
		fmt.Fprintf(&b, ".pct-%d{width:%d%%}", pct, pct)
	}
	return template.CSS(b.String())
}

// faviconPath is the Timlee mark, served by cmd/timlee-web from web/public
// with a validator, so a repeat visit costs a 304 at most.
const faviconPath = template.URL("/favicon.svg")

// Favicon is the document icon.
func (p *Page) Favicon() template.URL { return faviconPath }

// FillCSS is the proportional-width stylesheet this response carries.
func (p *Page) FillCSS() template.CSS { return FillCSS }

// FuncMap returns the helpers templates need that html/template does not
// provide. Merge the asset manifest's own FuncMap into this.
func FuncMap() template.FuncMap {
	funcs := template.FuncMap{
		"fillClass": FillClass,
		"percent":   Percent,
		"usd":       USD,
		"thousands": Thousands,
		"dict":      dict,
	}
	// t, tn, th and the locale formatters, which take .L first.
	maps.Copy(funcs, i18n.FuncMap())
	return funcs
}

// Percent is value as a percentage of total, clamped to 0–100.
func Percent(value, total float64) int {
	if total <= 0 {
		return 0
	}
	pct := int(value/total*100 + 0.5)
	return min(max(pct, 0), 100)
}

// FillClass names the width class nearest to value/total.
func FillClass(value, total float64) string {
	pct := Percent(value, total)
	// Rounded to the nearest step because only the steps exist as rules; an
	// unrounded class name would silently render a zero-width bar.
	rounded := (pct + fillStep/2) / fillStep * fillStep
	return "pct-" + strconv.Itoa(min(rounded, 100))
}

// USD formats whole dollars with thousands separators.
func USD(amount int) string { return "$" + Thousands(amount) }

// Thousands groups an integer for display.
func Thousands(n int) string {
	sign := ""
	if n < 0 {
		sign, n = "-", -n
	}
	digits := strconv.Itoa(n)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + b.String()
}

// dict builds a map so a partial can take named arguments, which
// html/template otherwise has no way to express.
func dict(pairs ...any) (map[string]any, error) {
	if len(pairs)%2 != 0 {
		return nil, fmt.Errorf("render: dict needs an even number of arguments, got %d", len(pairs))
	}
	out := make(map[string]any, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			return nil, fmt.Errorf("render: dict key %d is %T, want string", i, pairs[i])
		}
		out[key] = pairs[i+1]
	}
	return out, nil
}
