package i18n

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rahulguptaio/timlee/internal/i18n/cldr"
)

// The formatters reproduce what the browser's Intl prints for the same value
// in the same locale, from the same CLDR release: the server renders a
// number, a date or a duration, and an island that later updates it with
// Intl writes the identical string, so nothing moves. Each method names the
// Intl options it matches; testdata/intl-golden.json holds Intl's output and
// the tests require byte equality.
//
// Numbers are handled as decimal digit strings, never as binary fractions:
// a float64 enters as its shortest round-tripping decimal, as in ICU, so
// 1.005 rounds to 1.01 and 0.425 is 42.5%.

// DateStyle picks how much of a date is written.
type DateStyle uint8

const (
	// DateMedium is the day, month and year: "29 Oct 2026", "29.10.2026",
	// "2026/10/29". Intl {dateStyle: "medium"}.
	DateMedium DateStyle = iota
	// DateMonthDay is the day and abbreviated month: "29 Oct", "29. Okt.",
	// "10月29日". Intl {month: "short", day: "numeric"}.
	DateMonthDay
)

// Int formats an integer with the locale's digits and grouping:
// "1,234,567", "1.234.567", "1 234 567". Intl.NumberFormat(locale).
func (l *Localizer) Int(n int64) string {
	var buf [48]byte
	d := decimalOfInt(n)
	return string(l.f.decimal.format(buf[:0], &d, l.f))
}

// Decimal formats v with at most maxFrac fraction digits, rounding half away
// from zero: Intl.NumberFormat(locale, {maximumFractionDigits: maxFrac}).
func (l *Localizer) Decimal(v float64, maxFrac int) string {
	var buf [48]byte
	p := l.f.decimal
	p.maxFrac = min(max(maxFrac, 0), 20)
	if b, ok := l.f.special(buf[:0], v, &p); ok {
		return string(b)
	}
	d := decimalOfFloat(v)
	d.round(p.maxFrac)
	return string(p.format(buf[:0], &d, l.f))
}

// Percent formats a ratio as a whole percentage: 0.425 is "43%", "43 %".
// Intl.NumberFormat(locale, {style: "percent"}).
func (l *Localizer) Percent(ratio float64) string {
	var buf [48]byte
	p := &l.f.percent
	if b, ok := l.f.special(buf[:0], ratio, p); ok {
		return string(b)
	}
	d := decimalOfFloat(ratio)
	d.shift(2)
	d.round(p.maxFrac)
	return string(p.format(buf[:0], &d, l.f))
}

// USD formats an amount of US cents with the narrow dollar symbol and the
// locale's currency pattern: "$1,234.50", "1.234,50 $", and in Arabic a
// right-to-left mark before "1,234.50 US$".
// Prices are dollars in every locale (no conversion). wholeDollars rounds to
// the dollar, half away from zero. Intl.NumberFormat(locale, {style:
// "currency", currency: "USD", currencyDisplay: "narrowSymbol"}), plus
// {minimumFractionDigits: 0, maximumFractionDigits: 0} for whole dollars.
func (l *Localizer) USD(cents int64, wholeDollars bool) string {
	var buf [64]byte
	p := l.f.currency
	d := decimalOfInt(cents)
	d.shift(-2)
	if wholeDollars {
		p.minFrac, p.maxFrac = 0, 0
	}
	d.round(p.maxFrac)
	return string(p.format(buf[:0], &d, l.f))
}

// USDDecimal formats a dollar amount with caller-selected fraction digits.
// It is for sub-cent metered costs that cannot be represented as integer
// cents while retaining the locale's USD symbol placement and bidi marks.
func (l *Localizer) USDDecimal(dollars float64, minFrac, maxFrac int) string {
	var buf [64]byte
	p := l.f.currency
	p.minFrac = min(max(minFrac, 0), 20)
	p.maxFrac = min(max(maxFrac, p.minFrac), 20)
	if b, ok := l.f.special(buf[:0], dollars, &p); ok {
		return string(b)
	}
	d := decimalOfFloat(dollars)
	d.round(p.maxFrac)
	return string(p.format(buf[:0], &d, l.f))
}

// Compact abbreviates a count: "1.2K", "1,2 Mrd.", "12億". Intl.NumberFormat
// (locale, {notation: "compact"}).
func (l *Localizer) Compact(n int64) string {
	var buf [64]byte
	return string(l.f.compact(buf[:0], n))
}

// Date formats the date of t in t's location. Intl.DateTimeFormat with the
// options DateStyle names.
func (l *Localizer) Date(t time.Time, style DateStyle) string {
	var buf [64]byte
	layout := l.f.dateMedium
	if style == DateMonthDay {
		layout = l.f.monthDay
	}
	return string(l.f.appendDate(buf[:0], t, layout))
}

// Time formats the time of day of t in t's location and the locale's hour
// cycle: "14:05", "2:05 م". Intl {timeStyle: "short"}.
func (l *Localizer) Time(t time.Time) string {
	var buf [64]byte
	return string(l.f.appendDate(buf[:0], t, l.f.timeShort))
}

// DateTime formats a date and a time of day in t's location. DateMedium is
// Intl {dateStyle: "medium", timeStyle: "short"}: "29 Oct 2026, 14:05";
// DateMonthDay is Intl {month: "short", day: "numeric", hour: "2-digit",
// minute: "2-digit"}: "29 Oct, 14:05".
func (l *Localizer) DateTime(t time.Time, style DateStyle) string {
	var buf [96]byte
	layout := l.f.dateTimeMedium
	if style == DateMonthDay {
		layout = l.f.monthDayTime
	}
	return string(l.f.appendDate(buf[:0], t, layout))
}

// Relative describes t from now in the coarsest honest unit, truncating:
// seconds under a minute, minutes under an hour, hours under two days, days
// beyond. "5 minutes ago", "in 5 Minuten". Intl.RelativeTimeFormat(locale)
// .format(value, unit), negative for the past.
func (l *Localizer) Relative(t, now time.Time) string {
	d := now.Sub(t)
	past := d >= 0
	if !past {
		d = -d
	}
	var value int64
	var unit int
	switch {
	case d < time.Minute:
		value, unit = int64(d/time.Second), cldr.Second
	case d < time.Hour:
		value, unit = int64(d/time.Minute), cldr.Minute
	case d < 48*time.Hour:
		value, unit = int64(d/time.Hour), cldr.Hour
	default:
		value, unit = int64(d/(24*time.Hour)), cldr.Day
	}
	var buf [96]byte
	return string(l.f.appendRelative(buf[:0], value, unit, past))
}

// Duration formats a short span with the short unit names: whole
// milliseconds under a second, then seconds and then minutes to one decimal
// (half up), choosing the unit after rounding so 59.96 s is "1 min".
// "212 ms", "1.5 secs", "3 Min.". Intl.NumberFormat(locale, {style: "unit",
// unit}).
func (l *Localizer) Duration(d time.Duration) string {
	r := d.Round(time.Millisecond)
	abs := max(r, -r)
	unit, count, scale := cldr.Millisecond, int64(abs/time.Millisecond), 0
	if abs >= time.Second {
		unit, count, scale = cldr.UnitSecond, int64((abs+50*time.Millisecond)/(100*time.Millisecond)), 1
		if count >= 600 {
			unit, count = cldr.UnitMinute, int64((abs+3*time.Second)/(6*time.Second))
		}
	}
	v := decimalOfInt(count)
	v.shift(-scale)
	v.neg = r < 0
	var buf [64]byte
	return string(l.f.appendUnit(buf[:0], v, unit))
}

/* --------------------------------------------------------------- decimal -- */

// decimal is a number as decimal digits: value = 0.d[0]d[1]…d[n-1] × 10^dp.
// Digits carry no leading or trailing zeros; zero has n == 0.
type decimal struct {
	neg bool
	d   [24]byte
	n   int
	dp  int
}

func decimalOfInt(v int64) decimal {
	var d decimal
	u := uint64(v)
	if v < 0 {
		d.neg, u = true, -u
	}
	b := strconv.AppendUint(d.d[:0], u, 10)
	d.n, d.dp = len(b), len(b)
	d.trim()
	return d
}

// decimalOfFloat takes the shortest decimal that reads back as v, which is
// the representation ICU rounds.
func decimalOfFloat(v float64) decimal {
	var d decimal
	var tmp [32]byte
	d.neg = math.Signbit(v)
	s := strconv.AppendFloat(tmp[:0], math.Abs(v), 'e', -1, 64)
	mant, exp, _ := strings.Cut(string(s), "e")
	e, _ := strconv.Atoi(exp)
	for i := 0; i < len(mant); i++ {
		if c := mant[i]; c != '.' {
			d.d[d.n] = c
			d.n++
		}
	}
	d.dp = e + 1
	d.trim()
	return d
}

// trim drops leading and trailing zeros.
func (d *decimal) trim() {
	lead := 0
	for lead < d.n && d.d[lead] == '0' {
		lead++
	}
	if lead > 0 {
		copy(d.d[:], d.d[lead:d.n])
		d.n -= lead
		d.dp -= lead
	}
	for d.n > 0 && d.d[d.n-1] == '0' {
		d.n--
	}
	if d.n == 0 {
		d.dp = 0
	}
}

// round keeps at most frac fraction digits, rounding half away from zero
// (Intl's default "halfExpand").
func (d *decimal) round(frac int) {
	keep := d.dp + frac
	if keep >= d.n {
		return
	}
	if keep < 0 {
		d.n, d.dp = 0, 0
		return
	}
	up := d.d[keep] >= '5'
	d.n = keep
	if up {
		i := keep - 1
		for i >= 0 && d.d[i] == '9' {
			i--
		}
		if i < 0 {
			d.d[0], d.n = '1', 1
			d.dp++
			return
		}
		d.d[i]++
		d.n = i + 1
	}
	d.trim()
}

// magnitude is the power of ten of the leading digit; 0 for zero.
func (d *decimal) magnitude() int {
	if d.n == 0 {
		return 0
	}
	return d.dp - 1
}

// shift multiplies d by 10^k.
func (d *decimal) shift(k int) {
	if d.n > 0 {
		d.dp += k
	}
}

// operands are the plural operands of d as written with frac visible
// fraction digits at least (Intl's minimumFractionDigits).
func (d *decimal) operands(minFrac int) operands {
	var o operands
	for i := 0; i < d.dp; i++ {
		digit := uint64(0)
		if i < d.n {
			digit = uint64(d.d[i] - '0')
		}
		o.i = o.i*10 + digit
		if o.i >= bigMod {
			o.i, o.iBig = o.i%bigMod, true
		}
	}
	frac := max(d.n-d.dp, 0)
	o.w = frac
	o.v = max(frac, minFrac)
	for i := 0; i < o.v; i++ {
		digit := uint64(0)
		if j := d.dp + i; j >= 0 && j < d.n {
			digit = uint64(d.d[j] - '0')
		}
		o.f = o.f*10 + digit
		if o.f >= bigMod {
			o.f, o.fBig = o.f%bigMod, true
		}
		if i == o.w-1 {
			// t is f without the zeros minimumFractionDigits pads it with.
			o.t = o.f
		}
	}
	return o
}

/* -------------------------------------------------------------- patterns -- */

// numberPattern is a parsed CLDR number pattern with its symbols resolved.
type numberPattern struct {
	posPrefix, posSuffix string
	negPrefix, negSuffix string
	minFrac, maxFrac     int
	grouping, grouping2  int
	minGrouping          int
	// zeros counts the 0s of a compact pattern, which set its divisor.
	zeros int
}

// parseNumberPattern reads "#,##0.###", "¤#,##0.00", "#,##0 %",
// "#,##0.00 ¤;-#,##0.00 ¤" or a compact "00 Mio'.'", resolving ¤, %, - and +
// to the locale's symbols.
func parseNumberPattern(src string, f *formatter, currency string) (numberPattern, error) {
	var p numberPattern
	pos, neg, hasNeg := cutUnquoted(src, ';')
	prefix, number, suffix, err := splitPattern(pos)
	if err != nil {
		return p, fmt.Errorf("pattern %q: %w", src, err)
	}
	p.posPrefix = f.affix(prefix, currency)
	p.posSuffix = f.affix(suffix, currency)
	if hasNeg {
		nprefix, _, nsuffix, err := splitPattern(neg)
		if err != nil {
			return p, fmt.Errorf("pattern %q: %w", src, err)
		}
		p.negPrefix, p.negSuffix = f.affix(nprefix, currency), f.affix(nsuffix, currency)
	} else {
		p.negPrefix, p.negSuffix = f.minus+p.posPrefix, p.posSuffix
	}

	integer, fraction, _ := strings.Cut(number, ".")
	p.minFrac = strings.Count(fraction, "0")
	p.maxFrac = len(fraction)
	p.zeros = strings.Count(integer, "0")
	if last := strings.LastIndexByte(integer, ','); last >= 0 {
		p.grouping = len(integer) - last - 1
		p.grouping2 = p.grouping
		if prev := strings.LastIndexByte(integer[:last], ','); prev >= 0 {
			p.grouping2 = last - prev - 1
		}
	}
	p.minGrouping = f.minGrouping
	return p, nil
}

// splitPattern separates a subpattern's prefix, number and suffix.
func splitPattern(s string) (prefix, number, suffix string, err error) {
	start, end := -1, -1
	quoted := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case strings.IndexByte("#0123456789,.", c) >= 0:
			if start < 0 {
				start = i
			} else if end >= 0 {
				return "", "", "", fmt.Errorf("number at %d after the suffix began", i)
			}
		default:
			if start >= 0 && end < 0 {
				end = i
			}
		}
	}
	if start < 0 {
		return "", "", "", fmt.Errorf("no number")
	}
	if end < 0 {
		end = len(s)
	}
	return s[:start], s[start:end], s[end:], nil
}

func cutUnquoted(s string, sep byte) (before, after string, found bool) {
	quoted := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\'':
			quoted = !quoted
		case !quoted && s[i] == sep:
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// affix resolves an affix's quotes and symbols.
func (f *formatter) affix(s, currency string) string {
	var b strings.Builder
	quoted := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		switch {
		case r == '\'':
			if i < len(s) && s[i] == '\'' {
				b.WriteByte('\'')
				i++
			} else {
				quoted = !quoted
			}
		case quoted:
			b.WriteRune(r)
		case r == '¤':
			b.WriteString(currency)
		case r == '%':
			b.WriteString(f.percentSign)
		case r == '-':
			b.WriteString(f.minus)
		case r == '+':
			b.WriteString(f.plus)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// format writes d through the pattern.
func (p *numberPattern) format(b []byte, d *decimal, f *formatter) []byte {
	if d.neg {
		b = append(b, p.negPrefix...)
	} else {
		b = append(b, p.posPrefix...)
	}
	b = p.appendDigits(b, d, f)
	if d.neg {
		return append(b, p.negSuffix...)
	}
	return append(b, p.posSuffix...)
}

// appendDigits writes the integer digits, grouped, and the fraction.
func (p *numberPattern) appendDigits(b []byte, d *decimal, f *formatter) []byte {
	intLen := max(d.dp, 1)
	grouped := p.grouping > 0 && intLen-p.grouping >= p.minGrouping
	for i := 0; i < intLen; i++ {
		if grouped && i > 0 {
			left := intLen - i // digits from here to the decimal point
			if left == p.grouping || left > p.grouping && (left-p.grouping)%p.grouping2 == 0 {
				b = append(b, f.group...)
			}
		}
		if i < d.dp && i < d.n && d.dp > 0 {
			b = append(b, d.d[i])
		} else {
			b = append(b, '0')
		}
	}
	frac := max(d.n-d.dp, 0)
	frac = max(frac, p.minFrac)
	if frac > 0 {
		b = append(b, f.decimalSep...)
		for i := 0; i < frac; i++ {
			j := d.dp + i
			if j >= 0 && j < d.n {
				b = append(b, d.d[j])
			} else {
				b = append(b, '0')
			}
		}
	}
	return b
}

/* ------------------------------------------------------------- formatter -- */

// formatter is one formatting locale's data, parsed once.
type formatter struct {
	plural *pluralRules

	decimalSep, group, percentSign, minus, plus, infinity, nan string
	minGrouping                                                int

	decimal, percent, currency numberPattern
	// compactPatterns[m][c] is the pattern for 10^m in category c; a zero
	// value means none (fall back to Other, then to no abbreviation).
	compactPatterns [][cldr.Categories]*numberPattern
	compactNone     []bool

	months               [12]string
	am, pm               string
	dateMedium, monthDay []dateField
	timeShort            []dateField
	dateTimeMedium       []dateField
	monthDayTime         []dateField
	relative             [cldr.RelativeUnits][2][cldr.Categories]string
	units                [cldr.DurationUnits][cldr.Categories]string
	compactDecimal       numberPattern
}

// newFormatter parses one locale's CLDR data.
func newFormatter(src *cldr.Locale) (*formatter, error) {
	f := &formatter{
		decimalSep:  src.Decimal,
		group:       src.Group,
		percentSign: src.PercentSign,
		minus:       src.MinusSign,
		plus:        src.PlusSign,
		infinity:    src.Infinity,
		nan:         src.NaN,
		minGrouping: src.MinimumGroupingDigits,
		months:      src.Months,
		am:          src.AM,
		pm:          src.PM,
		relative:    src.Relative,
		units:       src.Units,
	}
	var err error
	if f.plural, err = parsePluralRules(src.PluralRules); err != nil {
		return nil, err
	}
	if f.decimal, err = parseNumberPattern(src.DecimalPattern, f, ""); err != nil {
		return nil, err
	}
	if f.percent, err = parseNumberPattern(src.PercentPattern, f, ""); err != nil {
		return nil, err
	}
	if f.currency, err = parseNumberPattern(src.CurrencyPattern, f, src.USD); err != nil {
		return nil, err
	}
	// Compact numbers group only from five digits ("min2"), as Intl does.
	f.compactDecimal = f.decimal
	f.compactDecimal.minGrouping = max(f.minGrouping, 2)

	f.compactPatterns = make([][cldr.Categories]*numberPattern, len(src.Compact))
	f.compactNone = make([]bool, len(src.Compact))
	for m, forms := range src.Compact {
		for c, s := range forms {
			switch s {
			case "":
				continue
			case "0":
				f.compactNone[m] = true
				continue
			}
			p, err := parseNumberPattern(s, f, "")
			if err != nil {
				return nil, err
			}
			f.compactPatterns[m][c] = &p
		}
	}

	for _, layout := range []struct {
		dst *[]dateField
		src string
	}{
		{&f.dateMedium, src.DateMedium},
		{&f.monthDay, src.MonthDay},
		{&f.timeShort, src.TimeShort},
		{&f.dateTimeMedium, src.DateTimeMedium},
		{&f.monthDayTime, src.MonthDayTime},
	} {
		if *layout.dst, err = parseDatePattern(layout.src); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// special writes NaN and the infinities as Intl does: the locale's symbol
// inside the pattern's affixes.
func (f *formatter) special(b []byte, v float64, p *numberPattern) ([]byte, bool) {
	switch {
	case math.IsNaN(v):
		b = append(b, p.posPrefix...)
		b = append(b, f.nan...)
		return append(b, p.posSuffix...), true
	case math.IsInf(v, 1):
		b = append(b, p.posPrefix...)
		b = append(b, f.infinity...)
		return append(b, p.posSuffix...), true
	case math.IsInf(v, -1):
		b = append(b, p.negPrefix...)
		b = append(b, f.infinity...)
		return append(b, p.negSuffix...), true
	}
	return b, false
}

// compactMultiplier is the power of ten a number of magnitude m is divided by
// (negated), as ICU's CompactData computes it: 0 where CLDR has no
// abbreviation.
func (f *formatter) compactMultiplier(m int) int {
	if m < 0 || len(f.compactPatterns) == 0 {
		return 0
	}
	m = min(m, len(f.compactPatterns)-1)
	if f.compactNone[m] {
		return 0
	}
	p := f.compactPatterns[m][cldr.Other]
	if p == nil {
		return 0
	}
	return p.zeros - m - 1
}

// compact follows ICU's CompactHandler: scale by the pattern's divisor, round
// to an integer keeping two significant digits, re-scale if rounding carried
// into a magnitude with another divisor, then pick the pattern by the
// rounded value's plural category.
func (f *formatter) compact(b []byte, n int64) []byte {
	d := decimalOfInt(n)
	mult := 0
	if d.n > 0 {
		mag := d.magnitude()
		mult = f.compactMultiplier(mag)
		d.dp += mult
		roundCompact(&d)
		if d.n > 0 && d.magnitude() != mag+mult {
			if next := f.compactMultiplier(mag + 1); next != mult {
				d.dp += next - mult
				roundCompact(&d)
				mult = next
			}
		}
	}
	magnitude := d.magnitude() - mult
	if d.n == 0 {
		magnitude = 0
	}

	p := &f.compactDecimal
	if magnitude >= 0 && len(f.compactPatterns) > 0 {
		m := min(magnitude, len(f.compactPatterns)-1)
		forms := &f.compactPatterns[m]
		if !f.compactNone[m] {
			c := f.plural.category(d.operands(0))
			if forms[c] != nil {
				p = forms[c]
			} else if forms[cldr.Other] != nil {
				p = forms[cldr.Other]
			}
		}
	}
	if d.neg {
		b = append(b, f.minus...)
	}
	b = append(b, p.posPrefix...)
	b = f.compactDecimal.appendDigits(b, &d, f)
	return append(b, p.posSuffix...)
}

// roundCompact rounds to an integer but keeps at least two significant
// digits: 1.234 → 1.2, 12.34 → 12.
func roundCompact(d *decimal) {
	if d.n == 0 {
		return
	}
	d.round(max(0, 1-d.magnitude()))
}

// appendRelative writes value units in the future or the past.
func (f *formatter) appendRelative(b []byte, value int64, unit int, past bool) []byte {
	tense := 0
	if past {
		tense = 1
	}
	forms := &f.relative[unit][tense]
	pattern := forms[f.plural.category(intOperands(value))]
	if pattern == "" {
		pattern = forms[cldr.Other]
	}
	d := decimalOfInt(value)
	return f.appendPattern(b, pattern, &d, &f.decimal)
}

// appendUnit writes a number of milliseconds, seconds or minutes.
func (f *formatter) appendUnit(b []byte, v decimal, unit int) []byte {
	v.round(3)
	forms := &f.units[unit]
	pattern := forms[f.plural.category(v.operands(0))]
	if pattern == "" {
		pattern = forms[cldr.Other]
	}
	return f.appendPattern(b, pattern, &v, &f.decimal)
}

// appendPattern substitutes the formatted number for {0}.
func (f *formatter) appendPattern(b []byte, pattern string, d *decimal, p *numberPattern) []byte {
	before, after, ok := strings.Cut(pattern, "{0}")
	b = append(b, before...)
	if ok {
		b = p.format(b, d, f)
		b = append(b, after...)
	}
	return b
}

/* ----------------------------------------------------------------- dates -- */

// dateField is one run of a CLDR date pattern: a field letter repeated
// count times, or literal text when letter is 0.
type dateField struct {
	letter byte
	count  int
	text   string
}

func parseDatePattern(s string) ([]dateField, error) {
	var out []dateField
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			out = append(out, dateField{text: lit.String()})
			lit.Reset()
		}
	}
	quoted := false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'':
			if i+1 < len(s) && s[i+1] == '\'' {
				lit.WriteByte('\'')
				i += 2
				continue
			}
			quoted = !quoted
			i++
		case quoted || !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'):
			lit.WriteByte(c)
			i++
		default:
			n := 1
			for i+n < len(s) && s[i+n] == c {
				n++
			}
			if !strings.ContainsRune("yMdHhKkma", rune(c)) || c == 'M' && n > 3 {
				return nil, fmt.Errorf("date pattern %q: unsupported field %q", s, s[i:i+n])
			}
			flush()
			out = append(out, dateField{letter: c, count: n})
			i += n
		}
	}
	flush()
	return out, nil
}

func (f *formatter) appendDate(b []byte, t time.Time, layout []dateField) []byte {
	year, month, day := t.Date()
	hour, minute, _ := t.Clock()
	for _, fl := range layout {
		switch fl.letter {
		case 0:
			b = append(b, fl.text...)
		case 'y':
			if fl.count == 2 {
				b = appendPadded(b, year%100, 2)
			} else {
				b = appendPadded(b, year, fl.count)
			}
		case 'M':
			if fl.count == 3 {
				b = append(b, f.months[month-1]...)
			} else {
				b = appendPadded(b, int(month), fl.count)
			}
		case 'd':
			b = appendPadded(b, day, fl.count)
		case 'H':
			b = appendPadded(b, hour, fl.count)
		case 'k':
			b = appendPadded(b, (hour+23)%24+1, fl.count)
		case 'h':
			b = appendPadded(b, (hour+11)%12+1, fl.count)
		case 'K':
			b = appendPadded(b, hour%12, fl.count)
		case 'm':
			b = appendPadded(b, minute, fl.count)
		case 'a':
			if hour < 12 {
				b = append(b, f.am...)
			} else {
				b = append(b, f.pm...)
			}
		}
	}
	return b
}

func appendPadded(b []byte, v, width int) []byte {
	if v < 0 {
		b = append(b, '-')
		v = -v
	}
	var tmp [20]byte
	s := strconv.AppendInt(tmp[:0], int64(v), 10)
	for i := len(s); i < width; i++ {
		b = append(b, '0')
	}
	return append(b, s...)
}
