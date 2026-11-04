package i18n

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rahulguptaio/timlee/internal/i18n/cldr"
)

// intlGolden is testdata/intl-golden.json, written by intl-golden.mjs from
// Node's Intl. Each row is the input and what Intl printed for it.
type intlGolden struct {
	CLDR    string `json:"cldr"`
	Locales map[string]struct {
		Int            [][]any `json:"int"`
		Decimal        [][]any `json:"decimal"`
		Percent        [][]any `json:"percent"`
		Compact        [][]any `json:"compact"`
		USD            [][]any `json:"usd"`
		Date           [][]any `json:"date"`
		MonthDay       [][]any `json:"monthDay"`
		Time           [][]any `json:"time"`
		DateTime       [][]any `json:"dateTime"`
		MonthDayTime   [][]any `json:"monthDayTime"`
		Relative       [][]any `json:"relative"`
		Unit           [][]any `json:"unit"`
		PluralFirst    string  `json:"pluralFirst"`
		PluralDigest   string  `json:"pluralDigest"`
		PluralDecimals [][]any `json:"pluralDecimals"`
	} `json:"locales"`
}

func loadIntlGolden(t *testing.T) intlGolden {
	t.Helper()
	raw, err := os.ReadFile("testdata/intl-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g intlGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	// The fixture must come from the CLDR release the tables were generated
	// from, or a difference is a version skew rather than a bug.
	if major, _, _ := strings.Cut(cldr.Version, "."); !strings.HasPrefix(g.CLDR, major+".") {
		t.Fatalf("fixture is CLDR %s, tables are CLDR %s: regenerate one of them", g.CLDR, cldr.Version)
	}
	return g
}

// localizerFor is a localizer with no catalog, for the formatters alone.
func localizerFor(t *testing.T, intl string) *Localizer {
	t.Helper()
	f := formatters[cldrID(intl)]
	if f == nil {
		t.Fatalf("no formatter for %s", intl)
	}
	return &Localizer{f: f}
}

// The formatting locales Timlee ships: every Locale.Intl in the table.
func TestEveryLocaleIsInTheFixture(t *testing.T) {
	g := loadIntlGolden(t)
	for _, loc := range table {
		if _, ok := g.Locales[loc.Intl]; !ok {
			t.Errorf("intl-golden.json has no %s (locale %s); add it to intl-golden.mjs", loc.Intl, loc.ID)
		}
	}
}

func TestFormattersMatchIntl(t *testing.T) {
	g := loadIntlGolden(t)
	for intl, want := range g.Locales {
		l := localizerFor(t, intl)
		t.Run(intl, func(t *testing.T) {
			check := func(kind, input, got, want string) {
				t.Helper()
				if got != want {
					t.Errorf("%s(%s) = %q, Intl %q", kind, input, got, want)
				}

			}
			for _, row := range want.Int {
				n, _ := strconv.ParseInt(row[0].(string), 10, 64)
				check("Int", row[0].(string), l.Int(n), row[1].(string))
			}
			for _, row := range want.Decimal {
				v, _ := strconv.ParseFloat(row[0].(string), 64)
				frac := int(row[1].(float64))
				check("Decimal", row[0].(string)+","+strconv.Itoa(frac), l.Decimal(v, frac), row[2].(string))
			}
			for _, row := range want.Percent {
				v, _ := strconv.ParseFloat(row[0].(string), 64)
				check("Percent", row[0].(string), l.Percent(v), row[1].(string))
			}
			for _, row := range want.Compact {
				n, _ := strconv.ParseInt(row[0].(string), 10, 64)
				check("Compact", row[0].(string), l.Compact(n), row[1].(string))
			}
			for _, row := range want.USD {
				cents, _ := strconv.ParseInt(row[0].(string), 10, 64)
				whole := row[1].(bool)
				check("USD", row[0].(string)+","+strconv.FormatBool(whole), l.USD(cents, whole), row[2].(string))
			}
			dates := []struct {
				kind string
				rows [][]any
				f    func(time.Time) string
			}{
				{"Date(medium)", want.Date, func(t time.Time) string { return l.Date(t, DateMedium) }},
				{"Date(monthDay)", want.MonthDay, func(t time.Time) string { return l.Date(t, DateMonthDay) }},
				{"Time", want.Time, l.Time},
				{"DateTime(medium)", want.DateTime, func(t time.Time) string { return l.DateTime(t, DateMedium) }},
				{"DateTime(monthDay)", want.MonthDayTime, func(t time.Time) string { return l.DateTime(t, DateMonthDay) }},
			}
			for _, d := range dates {
				for _, row := range d.rows {
					at := time.UnixMilli(int64(row[0].(float64))).UTC()
					check(d.kind, at.Format(time.RFC3339), d.f(at), row[1].(string))
				}
			}
			units := map[string]int{"second": cldr.Second, "minute": cldr.Minute, "hour": cldr.Hour, "day": cldr.Day}
			for _, row := range want.Relative {
				v, unit, past := int64(row[0].(float64)), row[1].(string), row[2].(bool)
				got := string(l.f.appendRelative(nil, v, units[unit], past))
				check("relative", strconv.FormatInt(v, 10)+" "+unit+" past="+strconv.FormatBool(past), got, row[3].(string))
			}
			durations := map[string]int{"millisecond": cldr.Millisecond, "second": cldr.UnitSecond, "minute": cldr.UnitMinute}
			for _, row := range want.Unit {
				v, _ := strconv.ParseFloat(row[0].(string), 64)
				got := string(l.f.appendUnit(nil, decimalOfFloat(v), durations[row[1].(string)]))
				check("unit", row[0].(string)+" "+row[1].(string), got, row[2].(string))
			}
		})
	}
}

func TestUSDDecimalKeepsLocaleCurrencyPattern(t *testing.T) {
	for _, tc := range []struct {
		intl string
		want string
	}{
		{"en-GB", "$0.0000094"},
		{"de", "0,0000094\u00a0$"},
		{"fr", "0,0000094\u00a0$"},
		{"ja", "$0.0000094"},
		{"ar-u-nu-latn", "\u200f0.0000094\u00a0US$"},
	} {
		if got := localizerFor(t, tc.intl).USDDecimal(0.0000094, 0, 20); got != tc.want {
			t.Errorf("%s USDDecimal = %q, want %q", tc.intl, got, tc.want)
		}
	}
}

// The plural rules pick Intl.PluralRules' category for every integer from 0
// to 1,000,000, compared through a digest of all of them, and for decimals
// with visible fraction digits.
func TestPluralRulesMatchIntl(t *testing.T) {
	g := loadIntlGolden(t)
	for intl, want := range g.Locales {
		rules := formatters[cldrID(intl)].plural
		t.Run(intl, func(t *testing.T) {
			t.Parallel()
			all := make([]byte, 1_000_001)
			for n := range all {
				all[n] = byte(rules.category(intOperands(int64(n))))
			}
			for n := 0; n < len(want.PluralFirst); n++ {
				if got := Category(all[n]); byte('0'+got) != want.PluralFirst[n] {
					t.Errorf("category(%d) = %s, Intl %s", n, got, Category(want.PluralFirst[n]-'0'))
				}
			}
			sum := sha256.Sum256(all)
			if got := hex.EncodeToString(sum[:]); got != want.PluralDigest {
				t.Errorf("categories of 0…1,000,000 differ from Intl's (digest %s, Intl %s)", got, want.PluralDigest)
			}
			for _, row := range want.PluralDecimals {
				s := row[0].(string)
				v, _ := strconv.ParseFloat(s, 64)
				_, frac, _ := strings.Cut(s, ".")
				d := decimalOfFloat(v)
				if got := rules.category(d.operands(len(frac))).String(); got != row[1].(string) {
					t.Errorf("category(%s) = %s, Intl %s", s, got, row[1])
				}
			}
		})
	}
}
