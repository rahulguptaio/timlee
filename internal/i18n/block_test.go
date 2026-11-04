package i18n

import (
	"encoding/json"
	"testing"
)

func TestBlockSendsMessagesAsParts(t *testing.T) {
	b := mustLoad(t, catalogs(nil), []string{"de"}, true)
	de, _ := b.ByID("de")
	xb, _ := b.ByID("ar-xb")

	tests := []struct {
		name string
		l    *Localizer
		keys []Key
		want string
	}{
		{
			name: "plural, arguments and a tag, in English",
			l:    b.For(nil),
			keys: []Key{"common.members.summary", "common.members.removed", "common.keys.lead", "common.nav.nowhere"},
			want: `{"lang":"en","intl":"en-GB","dir":"ltr","m":{` +
				`"common.keys.lead":["Call ",["endpoint"],", then read the guide."],` +
				`"common.members.removed":["Removed ",["name"]," from ",["org"],"."],` +
				`"common.members.summary":[["count","plural",{"=0":["No members"],"one":[["#"]," member"],"other":[["#"]," members"]}]],` +
				`"common.nav.nowhere":["⟦common.nav.nowhere⟧"]}}`,
		},
		{
			name: "a translation, and English where there is none",
			l:    b.For(de),
			keys: []Key{"common.nav.signIn", "common.nav.home"},
			want: `{"lang":"de","intl":"de","dir":"ltr","m":{"common.nav.home":["Home"],"common.nav.signIn":["Anmelden"]}}`,
		},
		{
			name: "a pseudo-locale's rewriting",
			l:    b.For(xb),
			keys: []Key{"common.members.removed"},
			want: `{"lang":"ar-XB","intl":"ar-u-nu-latn","dir":"rtl","m":{"common.members.removed":` +
				`["\u202eRemoved \u202c\u2066",["name"],"\u2069\u202e from \u202c\u2066",["org"],"\u2069\u202e.\u202c"]}}`,
		},
	}
	for _, tc := range tests {
		raw, err := json.Marshal(tc.l.Block(tc.keys...))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got, want := normalJSON(t, string(raw)), normalJSON(t, tc.want); got != want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, want)
		}
	}
}

// normalJSON is s re-encoded with its object keys sorted.
func normalJSON(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("not JSON: %v: %s", err, s)
	}
	out, _ := json.Marshal(v)
	return string(out)
}
