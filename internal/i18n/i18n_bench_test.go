package i18n

import (
	"bytes"
	"html/template"
	"testing"
	"time"
)

var (
	sinkString string
	sinkHTML   template.HTML
)

func benchLocalizer(b *testing.B, enabled []string, id string) *Localizer {
	b.Helper()
	extra := map[string]string{"ar/common.arb": `{"@@locale": "ar",
  "common.nav.signIn": "تسجيل الدخول",
  "common.members.summary": "{count, plural, =0 {لا أعضاء} zero {# عضو} one {عضو واحد} two {عضوان} few {# أعضاء} many {# عضوًا} other {# عضو}}",
  "common.members.removed": "تمت إزالة {name} من {org}."}`}
	bundle, err := Load(catalogs(extra), enabled, false)
	if err != nil {
		b.Fatal(err)
	}
	loc, ok := bundle.ByID(id)
	if !ok {
		b.Fatalf("no locale %s", id)
	}
	return bundle.For(loc)
}

// BenchmarkT is a message lookup: plain is most of a page's text and must
// not allocate; args and plural render a parsed message.
func BenchmarkT(b *testing.B) {
	for _, id := range []string{"en", "de", "ar"} {
		l := benchLocalizer(b, []string{"de", "ar"}, id)
		b.Run("plain/"+id, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkString = l.T("common.nav.signIn")
			}
		})
		b.Run("args/"+id, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkString = l.T("common.members.removed", "name", "Aiko", "org", "Acme")
			}
		})
		b.Run("plural/"+id, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkString = l.TN("common.members.summary", 1234)
			}
		})
	}
}

func BenchmarkHTML(b *testing.B) {
	l := benchLocalizer(b, nil, "en")
	b.Run("plain", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sinkHTML = l.HTML("common.nav.signIn")
		}
	})
	b.Run("rich", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sinkHTML = l.HTML("common.keys.lead", "endpoint", "https://api.example.com/v1", "link", "/docs")
		}
	})
}

// BenchmarkTemplateFuncs is t and tn as templates call them, through
// html/template's reflection and escaping.
func BenchmarkTemplateFuncs(b *testing.B) {
	l := benchLocalizer(b, nil, "en")
	for _, tc := range []struct{ name, src string }{
		{"t", `{{ t .L "common.nav.signIn" }}`},
		{"tn", `{{ tn .L "common.members.summary" .N }}`},
	} {
		tmpl := template.Must(template.New(tc.name).Funcs(FuncMap()).Parse(tc.src))
		data := map[string]any{"L": l, "N": 1234}
		var buf bytes.Buffer
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				buf.Reset()
				if err := tmpl.Execute(&buf, data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkFormat(b *testing.B) {
	when := time.Date(2026, 10, 29, 14, 5, 0, 0, time.UTC)
	for _, id := range []string{"en", "de", "ar"} {
		l := benchLocalizer(b, []string{"de", "ar"}, id)
		for _, f := range []struct {
			name string
			fn   func() string
		}{
			{"int", func() string { return l.Int(1234567) }},
			{"decimal", func() string { return l.Decimal(1234567.891, 2) }},
			{"percent", func() string { return l.Percent(0.425) }},
			{"compact", func() string { return l.Compact(1234567891) }},
			{"usd", func() string { return l.USD(123450, false) }},
			{"date", func() string { return l.Date(when, DateMedium) }},
			{"datetime", func() string { return l.DateTime(when, DateMedium) }},
			{"relative", func() string { return l.Relative(when, when.Add(5*time.Minute)) }},
			{"duration", func() string { return l.Duration(1500 * time.Millisecond) }},
		} {
			b.Run(f.name+"/"+id, func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					sinkString = f.fn()
				}
			})
		}
	}
}
