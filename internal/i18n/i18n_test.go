package i18n

import (
	"bytes"
	"context"
	"html/template"
	"strings"
	"testing"
	"testing/fstest"
	"time"
	"unicode/utf8"

	"golang.org/x/text/language"

	"github.com/rahulguptaio/timlee/web/locales"
)

// catalogs is a catalog tree for tests: English, and German with one message
// left untranslated.
func catalogs(extra map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{
		"en/common.arb": {Data: []byte(`{
  "@@locale": "en",
  "common.nav.signIn": "Sign in",
  "@common.nav.signIn": {"description": "Link to the sign-in page."},
  "common.nav.home": "Home",
  "common.members.summary": "{count, plural, =0 {No members} one {# member} other {# members}}",
  "@common.members.summary": {"placeholders": {"count": {"type": "int"}}},
  "common.members.removed": "Removed {name} from {org}.",
  "common.keys.lead": "Call <code>{endpoint}</code>, then read <link>the guide</link>."
}`)},
		"de/common.arb": {Data: []byte(`{
  "@@locale": "de",
  "common.nav.signIn": "Anmelden",
  "common.members.summary": "{count, plural, =0 {Keine Mitglieder} one {# Mitglied} other {# Mitglieder}}",
  "common.members.removed": "{name} aus {org} entfernt.",
  "common.keys.lead": "Rufen Sie <code>{endpoint}</code> auf und lesen Sie <link>die Anleitung</link>."
}`)},
	}
	for name, data := range extra {
		fsys[name] = &fstest.MapFile{Data: []byte(data)}
	}
	return fsys
}

func mustLoad(t *testing.T, fsys fstest.MapFS, enabled []string, pseudo bool) *Bundle {
	t.Helper()
	b, err := Load(fsys, enabled, pseudo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return b
}

func TestShippedCatalogsLoad(t *testing.T) {
	for _, pseudo := range []bool{false, true} {
		b, err := Load(locales.FS(), nil, pseudo)
		if err != nil {
			t.Fatalf("Load(pseudo=%v): %v", pseudo, err)
		}
		if got := b.Default(); got.ID != "en" || got.Lang != "en" || got.Dir != "ltr" || got.Intl != "en-GB" {
			t.Errorf("default locale = %+v", got)
		}
		_, hasPseudo := b.ByID("en-xa")
		if hasPseudo != pseudo {
			t.Errorf("pseudo=%v but en-xa enabled=%v", pseudo, hasPseudo)
		}
	}
}

func TestLoadEnablesTheDefaultAndOnlyWhatIsAsked(t *testing.T) {
	b := mustLoad(t, catalogs(nil), []string{" DE ", "de"}, false)
	if b.Default().ID != "en" {
		t.Fatalf("default = %s", b.Default().ID)
	}
	if _, ok := b.ByID("de"); !ok {
		t.Error("de not enabled")
	}
	for _, id := range []string{"fr", "en-xa", "ar-xb"} {
		if _, ok := b.ByID(id); ok {
			t.Errorf("%s enabled without being asked for", id)
		}
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name    string
		enabled []string
		pseudo  bool
		extra   map[string]string
		want    string
	}{
		{name: "unknown locale", enabled: []string{"xx"}, want: `locale "xx" is not one Timlee knows`},
		{name: "pseudo by name", enabled: []string{"en-xa"}, want: "TIMLEE_PSEUDO_LOCALES"},
		{name: "no catalog", enabled: []string{"fr"}, want: `locale "fr" has no catalog`},
		{name: "not JSON", enabled: []string{"de"}, extra: map[string]string{"de/common.arb": `{`}, want: "de/common.arb"},
		{name: "wrong @@locale", extra: map[string]string{"en/auth.arb": `{"@@locale": "de"}`}, want: "@@locale"},
		{name: "key outside its file", extra: map[string]string{"en/auth.arb": `{"@@locale": "en", "common.x.y": "Hi"}`}, want: "must be auth.<area>.<item>"},
		{name: "key not camel case", extra: map[string]string{"en/auth.arb": `{"@@locale": "en", "auth.sign_in.x": "Hi"}`}, want: "must be auth.<area>.<item>"},
		{name: "message does not parse", extra: map[string]string{"en/auth.arb": `{"@@locale": "en", "auth.x.y": "Hi {name"}`}, want: "auth.x.y"},
		{name: "metadata without message", extra: map[string]string{"en/auth.arb": `{"@@locale": "en", "@auth.x.y": {}}`}, want: "has no message"},
		{name: "undeclared placeholder", extra: map[string]string{"en/auth.arb": `{"@@locale": "en", "auth.x.y": "{a}", "@auth.x.y": {"placeholders": {"b": {}}}}`}, want: `argument "a" is not declared`},
		{
			name: "translation drops an argument", enabled: []string{"de"},
			extra: map[string]string{"de/common.arb": `{"@@locale": "de", "common.members.removed": "{name} entfernt."}`},
			want:  `argument "org" is missing`,
		},
		{
			name: "translation adds an argument", enabled: []string{"de"},
			extra: map[string]string{"de/common.arb": `{"@@locale": "de", "common.nav.home": "Start {x}"}`},
			want:  `argument "x" is not in the English message`,
		},
		{
			name: "translation changes tags", enabled: []string{"de"},
			extra: map[string]string{"de/common.arb": `{"@@locale": "de", "common.keys.lead": "<b>{endpoint}</b> <link>x</link>"}`},
			want:  "tags",
		},
		{
			name: "category the language lacks", enabled: []string{"de"},
			extra: map[string]string{"de/common.arb": `{"@@locale": "de", "common.members.summary": "{count, plural, few {#} other {#}}"}`},
			want:  "plural category few does not exist in de",
		},
		{
			name: "key English lacks", enabled: []string{"de"},
			extra: map[string]string{"de/common.arb": `{"@@locale": "de", "common.nav.extra": "x"}`},
			want:  "not in the English catalog",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(catalogs(tc.extra), tc.enabled, tc.pseudo)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestLocalizerRendersAndFallsBack(t *testing.T) {
	b := mustLoad(t, catalogs(nil), []string{"de"}, false)
	en := b.For(nil)
	de, _ := b.ByID("de")
	l := b.For(de)

	if got := en.T("common.nav.signIn"); got != "Sign in" {
		t.Errorf("en T = %q", got)
	}
	if got := l.T("common.nav.signIn"); got != "Anmelden" {
		t.Errorf("de T = %q", got)
	}
	// Untranslated: English, rather than nothing.
	if got := l.T("common.nav.home"); got != "Home" {
		t.Errorf("de fallback = %q", got)
	}
	if got := l.T("common.nav.nowhere"); got != "⟦common.nav.nowhere⟧" {
		t.Errorf("missing key = %q", got)
	}
	if got := l.TN("common.members.summary", 1234); got != "1.234 Mitglieder" {
		t.Errorf("de TN = %q", got)
	}
	if got := en.TN("common.members.summary", 0); got != "No members" {
		t.Errorf("en TN(0) = %q", got)
	}
	if got := l.T("common.members.removed", "name", "Aiko", "org", "Acme"); got != "\u2068Aiko\u2069 aus \u2068Acme\u2069 entfernt." {
		t.Errorf("de T with arguments = %q", got)
	}
	want := template.HTML(`Call <code dir="ltr"><bdi>/v1</bdi></code>, then read <a href="/docs">the guide</a>.`)
	if got := en.HTML("common.keys.lead", "endpoint", "/v1", "link", "/docs"); got != want {
		t.Errorf("HTML = %q", got)
	}
	if got := en.HTML("common.nav.nowhere"); got != "⟦common.nav.nowhere⟧" {
		t.Errorf("missing HTML = %q", got)
	}
	if b.For(&Locale{ID: "de"}) != en {
		t.Error("For accepted a locale from another bundle")
	}
}

// The common path, a message without arguments, must not allocate: it is
// most of every page's text.
func TestStaticMessagesDoNotAllocate(t *testing.T) {
	l := mustLoad(t, catalogs(nil), nil, false).For(nil)
	if n := testing.AllocsPerRun(100, func() { _ = l.T("common.nav.signIn") }); n != 0 {
		t.Errorf("T allocated %v times", n)
	}
	if n := testing.AllocsPerRun(100, func() { _ = l.HTML("common.nav.signIn") }); n != 0 {
		t.Errorf("HTML allocated %v times", n)
	}
}

func TestPseudoLocales(t *testing.T) {
	b := mustLoad(t, catalogs(nil), nil, true)
	xa, _ := b.ByID("en-xa")
	xb, _ := b.ByID("ar-xb")
	if xa.Lang != "en-XA" || xa.Dir != "ltr" || xb.Lang != "ar-XB" || xb.Dir != "rtl" || !xa.Pseudo || !xb.Pseudo {
		t.Fatalf("pseudo-locales: %+v %+v", xa, xb)
	}
	accented, mirrored := b.For(xa), b.For(xb)

	tests := []struct {
		l    *Localizer
		got  string
		want string
	}{
		// The example of docs/I18N.md §9.
		{accented, accented.T("common.nav.signIn"), "[Šîĝñ îñ·······]"},
		{accented, accented.T("common.members.removed", "name", "Aiko", "org", "Acme"), "[Ŕéɱöṽéð \u2068Aiko\u2069 ƒŕöɱ \u2068Acme\u2069.············]"},
		// A plural that is the whole message is a sentence per branch.
		{accented, accented.TN("common.members.summary", 2), "[2 ɱéɱƀéŕš········]"},
		{accented, accented.TN("common.members.summary", 0), "[Ñö ɱéɱƀéŕš··········]"},
		{mirrored, mirrored.T("common.nav.signIn"), "\u202eSign in\u202c"},
		{mirrored, mirrored.T("common.members.removed", "name", "Aiko", "org", "Acme"),
			"\u202eRemoved \u202c\u2066\u2068Aiko\u2069\u2069\u202e from \u202c\u2066\u2068Acme\u2069\u2069\u202e.\u202c"},
		// Arabic plural rules: 3 is "few", which English lacks, so it
		// falls back to "other".
		{mirrored, mirrored.TN("common.members.summary", 3), "\u2066" + "3" + "\u2069\u202e members\u202c"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.l.Locale().ID, tc.got, tc.want)
		}
	}
	if got := accented.HTML("common.keys.lead", "endpoint", "/v1", "link", "/docs"); !strings.HasPrefix(string(got), `[Çåļļ <code dir="ltr"><bdi>/v1</bdi></code>`) {
		t.Errorf("accented HTML = %q", got)
	}
	// Formatting follows the locale's Intl locale: Latin digits for ar-XB.
	if got := mirrored.Int(1234567); got != "1,234,567" {
		t.Errorf("ar-XB Int = %q", got)
	}
}

// Every pseudo-localized sentence is at least 35% longer than its English,
// so a layout that survives en-XA survives any real language.
func TestPseudoExpansion(t *testing.T) {
	for _, src := range []string{"OK", "Sign in", "Manage your API keys", "Removed {name} from the organisation.",
		"Timlee's control plane is not answering, so your account cannot be shown or changed. Nothing has been lost."} {
		nodes, err := parseMessage(src)
		if err != nil {
			t.Fatal(err)
		}
		english := textLength(nodes)
		pseudo := textLength(expand(nodes)) - 2 // the brackets
		if float64(pseudo) < 1.35*float64(english) {
			t.Errorf("%q: %d characters became %d", src, english, pseudo)
		}
	}
}

func TestNegotiate(t *testing.T) {
	b := mustLoad(t, catalogs(map[string]string{"ar/common.arb": `{"@@locale": "ar"}`}), []string{"de", "ar"}, true)
	tests := []struct {
		header string
		want   string
		sure   bool
	}{
		{"de-CH, en;q=0.5", "de", true},
		{"ar-EG,ar;q=0.9", "ar", true},
		{"fr-FR, fr;q=0.9", "en", false},
		{"en-XA", "en", true}, // a pseudo-locale is never suggested
		{"", "en", false},
		{"!!", "en", false},
	}
	for _, tc := range tests {
		loc, c := b.Negotiate(tc.header)
		if loc.ID != tc.want || (c >= language.High) != tc.sure {
			t.Errorf("Negotiate(%q) = %s (%v), want %s, confident=%v", tc.header, loc.ID, c, tc.want, tc.sure)
		}
	}
}

func TestContextCarriesTheLocale(t *testing.T) {
	b := mustLoad(t, catalogs(nil), []string{"de"}, false)
	if loc, fromURL := FromContext(context.Background()); loc != nil || fromURL {
		t.Fatal("empty context has a locale")
	}
	de, _ := b.ByID("de")
	loc, fromURL := FromContext(WithLocale(context.Background(), de, true))
	if loc != de || !fromURL || loc.Localizer() != b.For(de) {
		t.Fatalf("FromContext = %v, %v", loc, fromURL)
	}
}

func TestRelativeAndDurationPickTheirUnit(t *testing.T) {
	l := localizerFor(t, "en-GB")
	now := time.Date(2026, 10, 29, 14, 5, 0, 0, time.UTC)
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{0, "0 seconds ago"},
		{59 * time.Second, "59 seconds ago"},
		{90 * time.Second, "1 minute ago"},
		{-5 * time.Minute, "in 5 minutes"},
		{47*time.Hour + 59*time.Minute, "47 hours ago"},
		{48 * time.Hour, "2 days ago"},
		{-400 * 24 * time.Hour, "in 400 days"},
	} {
		if got := l.Relative(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("Relative(%v ago) = %q, want %q", tc.ago, got, tc.want)
		}
	}
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0 ms"},
		{212*time.Millisecond + 400*time.Microsecond, "212 ms"},
		{999600 * time.Microsecond, "1 sec"},
		{1500 * time.Millisecond, "1.5 secs"},
		{59960 * time.Millisecond, "1 min"},
		{90 * time.Second, "1.5 mins"},
		{-250 * time.Millisecond, "-250 ms"},
	} {
		if got := l.Duration(tc.d); got != tc.want {
			t.Errorf("Duration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestTemplateFuncs(t *testing.T) {
	b := mustLoad(t, catalogs(nil), []string{"de"}, false)
	de, _ := b.ByID("de")
	when := time.Date(2026, 10, 29, 14, 5, 0, 0, time.UTC)
	page := map[string]any{"L": b.For(de), "When": when, "Name": "Aiko", "N": 3, "Ratio": 0.425, "Took": 1500 * time.Millisecond}

	const src = `{{ t .L "common.nav.signIn" }}|{{ tn .L "common.members.summary" .N }}|` +
		`{{ th .L "common.keys.lead" "endpoint" "/v1" "link" "/docs" }}|{{ t .L "common.members.removed" "name" .Name "org" (num .L 1234) }}|` +
		`{{ num .L 1234567 }} {{ num .L 1.5 }} {{ pct .L .Ratio }} {{ compact .L 1234567891 }}|` +
		`{{ date .L .When }} {{ date .L .When "monthDay" }} {{ time .L .When }} {{ datetime .L .When }}|` +
		`{{ dur .L .Took }}|<a title="{{ isolate .Name }}">`
	tmpl := template.Must(template.New("x").Funcs(FuncMap()).Parse(src))
	var out bytes.Buffer
	if err := tmpl.Execute(&out, page); err != nil {
		t.Fatal(err)
	}
	want := "Anmelden|3 Mitglieder|" +
		`Rufen Sie <code dir="ltr"><bdi>/v1</bdi></code> auf und lesen Sie <a href="/docs">die Anleitung</a>.|` +
		"\u2068Aiko\u2069 aus 1.234 entfernt.|" +
		"1.234.567 1,5 43\u00a0% 1,2\u00a0Mrd.|" +
		"29.10.2026 29. Okt. 14:05 29.10.2026, 14:05|" +
		"1,5 Sek.|" + "<a title=\"\u2068Aiko\u2069\">"
	if got := out.String(); got != want {
		t.Errorf("template output\n got %q\nwant %q", got, want)
	}

	for _, bad := range []string{
		`{{ t .Nil "common.nav.signIn" }}`,
		`{{ tn .L "common.members.summary" "three" }}`,
		`{{ t .L "common.members.removed" "name" }}`,
		`{{ date .L .When "full" }}`,
	} {
		tmpl := template.Must(template.New("x").Funcs(FuncMap()).Parse(bad))
		data := map[string]any{"L": b.For(de), "Nil": (*Localizer)(nil), "When": when}
		if err := tmpl.Execute(&bytes.Buffer{}, data); err == nil {
			t.Errorf("%s rendered without an error", bad)
		}
	}
	if !utf8.ValidString(Isolate("x")) {
		t.Error("Isolate wrote invalid UTF-8")
	}
}
