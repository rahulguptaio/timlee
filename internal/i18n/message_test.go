package i18n

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/messages.golden")

func TestParseRejectsMalformedMessages(t *testing.T) {
	tests := []struct {
		src, want string
	}{
		{"Hello {name", "expected } or ,"},
		{"Hello }", "unmatched }"},
		{"{}", "argument without a name"},
		{"{n, number}", `unsupported type "number"`},
		{"{n, plural, one {x}}", "no other branch"},
		{"{n, select, a {x}}", "no other branch"},
		{"{n, plural, other {x} other {y}}", `branch "other" twice`},
		{"{n, plural, several {x} other {y}}", `"several" is not a plural category`},
		{"{n, plural, offset:1 other {x}}", "offset is not supported"},
		{"{n, select, =1 {x} other {y}}", "bad exact branch"},
		{"{n, plural, one x other {y}}", "needs a {message}"},
		{"{n, plural, one {x other {y}}", "expected a branch keyword"},
		{"{n, plural, other {x", "branch is not closed"},
		{"<b>bold", "<b> is not closed"},
		{"<b>bold</em>", "</em> closes nothing open"},
		{"<strong>x</strong>", "unknown tag <strong>"},
		{`<a href="x">x</a>`, "has no attributes"},
		{"</b>", "closes nothing open"},
		{"{n, plural, other {<b>x}}", "unmatched }"},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			_, err := parseMessage(tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseMessage(%q) error = %v, want one containing %q", tc.src, err, tc.want)
			}
		})
	}
}

func TestRenderMessages(t *testing.T) {
	en, ar := formatters["en-GB"], formatters["ar"]
	tests := []struct {
		name  string
		src   string
		args  []any
		f     *formatter
		plain string
		html  string
	}{
		{name: "text", src: "Sign in", plain: "Sign in", html: "Sign in"},
		{name: "escaped", src: `Tom & "Jerry" <3`, plain: `Tom & "Jerry" <3`, html: "Tom &amp; &#34;Jerry&#34; &lt;3"},
		{
			name: "string argument is isolated", src: "Removed {name}.", args: []any{"name", "عائشة"},
			plain: "Removed \u2068عائشة\u2069.", html: "Removed <bdi>عائشة</bdi>.",
		},
		{
			name: "markup in an argument is escaped", src: "Hi {name}", args: []any{"name", "<b>x</b>"},
			plain: "Hi \u2068<b>x</b>\u2069", html: "Hi <bdi>&lt;b&gt;x&lt;/b&gt;</bdi>",
		},
		{
			name: "formatted values are not isolated", src: "Updated {when}", args: []any{"when", Formatted("29 Oct 2026")},
			plain: "Updated 29 Oct 2026", html: "Updated 29 Oct 2026",
		},
		{name: "integers are formatted", src: "{n} tokens", args: []any{"n", 1234567}, plain: "1,234,567 tokens", html: "1,234,567 tokens"},
		{
			name: "plural one", src: "{count, plural, one {# key} other {# keys}}", args: []any{"count", 1},
			plain: "1 key", html: "1 key",
		},
		{
			name: "plural other with grouping", src: "{count, plural, one {# key} other {# keys}}", args: []any{"count", 1200},
			plain: "1,200 keys", html: "1,200 keys",
		},
		{
			name: "exact match wins", src: "{count, plural, =0 {No keys} one {# key} other {# keys}}", args: []any{"count", 0},
			plain: "No keys", html: "No keys",
		},
		{
			name: "arabic categories", src: "{count, plural, zero {z} one {o} two {t} few {f#} many {m#} other {x#}}", args: []any{"count", 11},
			f: ar, plain: "m11", html: "m11",
		},
		{
			name: "arabic falls back to other", src: "{count, plural, one {# key} other {# keys}}", args: []any{"count", 2},
			f: ar, plain: "2 keys", html: "2 keys",
		},
		{
			name: "select", src: "Keys{org, select, none {.} other { for {org}.}}", args: []any{"org", "Acme"},
			plain: "Keys for \u2068Acme\u2069.", html: "Keys for <bdi>Acme</bdi>.",
		},
		{
			name: "select keyword", src: "Keys{org, select, none {.} other { for {org}.}}", args: []any{"org", "none"},
			plain: "Keys.", html: "Keys.",
		},
		{
			name: "# in a select inside a plural", src: "{n, plural, other {{g, select, a {# a} other {# b}}}}", args: []any{"n", 3, "g", "a"},
			plain: "3 a", html: "3 a",
		},
		{
			name: "tags", src: "Call <code>{endpoint}</code>, <b>now</b>", args: []any{"endpoint", "/v1"},
			plain: "Call \u2068/v1\u2069, now", html: `Call <code dir="ltr"><bdi>/v1</bdi></code>, <b>now</b>`,
		},
		{
			name: "link", src: "Read <link>the docs</link>.", args: []any{"link", "/docs"},
			plain: "Read the docs.", html: `Read <a href="/docs">the docs</a>.`,
		},
		{
			name: "unsafe link", src: "<link>x</link>", args: []any{"link", "javascript:alert(1)"},
			plain: "x", html: `<a href="#ZgotmplZ">x</a>`,
		},
		{name: "quoting", src: "It''s '{literal}' and '#' and '<b>' it's", plain: "It's {literal} and # and <b> it's", html: "It&#39;s {literal} and # and &lt;b&gt; it&#39;s"},
		{name: "pound outside plural", src: "Issue #4", plain: "Issue #4", html: "Issue #4"},
		{name: "less-than", src: "a < b", plain: "a < b", html: "a &lt; b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			if f == nil {
				f = en
			}
			nodes, err := parseMessage(tc.src)
			if err != nil {
				t.Fatal(err)
			}
			for _, html := range []bool{false, true} {
				r := renderer{f: f, html: html, args: args{pairs: tc.args}}
				got := string(r.appendNodes(nil, nodes))
				want := tc.plain
				if html {
					want = tc.html
				}
				if r.err != nil || got != want {
					t.Errorf("html=%v: got %q (err %v), want %q", html, got, r.err, want)
				}
			}
		})
	}
}

func TestRenderReportsMisuse(t *testing.T) {
	tests := []struct {
		src  string
		args []any
		want string
	}{
		{"Hi {name}", nil, `argument "name" is missing`},
		{"{n, plural, other {#}}", []any{"n", "three"}, "needs an integer"},
		{"{n}", []any{"n", 1.5}, "pass a string, an integer or a Formatted"},
		{"<link>x</link>", nil, "needs a string argument"},
	}
	for _, tc := range tests {
		nodes, err := parseMessage(tc.src)
		if err != nil {
			t.Fatal(err)
		}
		r := renderer{f: formatters["en-GB"], html: true, args: args{pairs: tc.args}}
		r.appendNodes(nil, nodes)
		if r.err == nil || !strings.Contains(r.err.Error(), tc.want) {
			t.Errorf("%q with %v: error %v, want %q", tc.src, tc.args, r.err, tc.want)
		}
	}
}

// TestParseGolden pins the parsed form of a corpus of messages, including
// the ones that must fail, so a parser change shows up as a diff.
func TestParseGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/messages.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		src, err := strconv.Unquote(line)
		if err != nil {
			t.Fatalf("testdata/messages.txt: %s: %v", line, err)
		}
		fmt.Fprintf(&out, "%s\n", line)
		if nodes, err := parseMessage(src); err != nil {
			fmt.Fprintf(&out, "\terror: %v\n", err)
		} else {
			fmt.Fprintf(&out, "\t%s\n", dump(nodes))
		}
	}
	const golden = "testdata/messages.golden"
	if *update {
		if err := os.WriteFile(golden, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != string(want) {
		t.Errorf("parsed messages differ from %s; run go test ./internal/i18n -run TestParseGolden -update and review the diff\n%s", golden, out.String())
	}
}

// dump writes a parse tree compactly: text quoted, {arg}, #, <tag>…</tag>.
func dump(nodes []node) string {
	var b strings.Builder
	for i, n := range nodes {
		if i > 0 {
			b.WriteByte(' ')
		}
		switch n.kind {
		case textNode:
			b.WriteString(strconv.Quote(n.text))
		case argNode:
			b.WriteString("{" + n.text + "}")
		case poundNode:
			b.WriteString("#")
		case pluralNode, selectNode:
			kind := "plural"
			if n.kind == selectNode {
				kind = "select"
			}
			fmt.Fprintf(&b, "{%s %s", n.text, kind)
			for _, br := range n.branches {
				fmt.Fprintf(&b, " %s[%s]", br.key, dump(br.nodes))
			}
			b.WriteString("}")
		case tagNode:
			fmt.Fprintf(&b, "<%s>%s</%s>", n.text, dump(n.children), n.text)
		}
	}
	return b.String()
}

// format writes a parse tree back as a message, quoting every syntax
// character, for the round trip the fuzz test checks.
func format(nodes []node, inPlural bool) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.kind {
		case textNode:
			// Byte by byte: every syntax character is ASCII, and invalid
			// UTF-8 must survive as it is. A run of syntax characters is
			// quoted together, because '' inside a quote is an apostrophe.
			quoted := false
			for i := 0; i < len(n.text); i++ {
				switch c := n.text[i]; {
				case c == '\'':
					b.WriteString("''")
				case strings.IndexByte("{}<", c) >= 0 || c == '#' && inPlural:
					if !quoted {
						b.WriteByte('\'')
						quoted = true
					}
					b.WriteByte(c)
				default:
					if quoted {
						b.WriteByte('\'')
						quoted = false
					}
					b.WriteByte(c)
				}
			}
			if quoted {
				b.WriteByte('\'')
			}
		case argNode:
			b.WriteString("{" + n.text + "}")
		case poundNode:
			b.WriteString("#")
		case pluralNode, selectNode:
			kind := "plural"
			if n.kind == selectNode {
				kind = "select"
			}
			fmt.Fprintf(&b, "{%s, %s,", n.text, kind)
			for _, br := range n.branches {
				fmt.Fprintf(&b, " %s {%s}", br.key, format(br.nodes, inPlural || n.kind == pluralNode))
			}
			b.WriteString("}")
		case tagNode:
			fmt.Fprintf(&b, "<%s>%s</%s>", n.text, format(n.children, inPlural), n.text)
		}
	}
	return b.String()
}

func FuzzParseMessage(f *testing.F) {
	for _, seed := range []string{
		"Sign in", "Removed {name}.", "{count, plural, =0 {No keys} one {# key} other {# keys}}",
		"Keys{org, select, none {.} other { for {org}.}}", "Call <code>{e}</code> <link>docs</link>",
		"It''s '{x}' #", "{n, plural, other {{g, select, a {#} other {<b>#</b>}}}}", "a < b </b>", "'", "'{",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		nodes, err := parseMessage(src)
		if err != nil {
			return
		}
		again, err := parseMessage(format(nodes, false))
		if err != nil {
			t.Fatalf("%q parsed, but its re-formatted form %q does not: %v", src, format(nodes, false), err)
		}
		if !reflect.DeepEqual(nodes, again) {
			t.Fatalf("%q does not round-trip:\n%s\n%s", src, dump(nodes), dump(again))
		}
		// Whatever arguments arrive, rendering reports misuse; it never
		// panics.
		for _, html := range []bool{false, true} {
			r := renderer{f: formatters["ar"], html: html, args: args{pairs: []any{"count", 3, "name", "x", "link", "/"}}}
			r.appendNodes(nil, nodes)
		}
	})
}
