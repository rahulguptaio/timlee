package assets_test

import (
	"encoding/json"
	"html/template"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/rahulguptaio/timlee/internal/assets"
)

func TestManifestResolution(t *testing.T) {
	m := testManifest(t, embeddedFS(t))

	tests := []struct {
		name          string
		logical       string
		wantURL       string
		wantFile      string
		wantIntegrity string
		wantFound     bool
	}{
		{
			name:          "typescript entry resolves to its javascript output",
			logical:       "app.js",
			wantURL:       "/assets/app-TVWDLM6W.js",
			wantFile:      "app-TVWDLM6W.js",
			wantIntegrity: "sha384-T2hO7zKkY8LxiAArzsPXNjINCGbwMTu1In0Aopy4beZlBxSrGoWPBhGCG2rh7418",
			wantFound:     true,
		},
		{
			name:          "css entry resolves",
			logical:       "app.css",
			wantURL:       "/assets/app-FEPGMDJK.css",
			wantFile:      "app-FEPGMDJK.css",
			wantIntegrity: "sha384-EVzoo4dggmrIott4oRm5M49WHOszBef4lUVDn4Dss17xC3M8m9+9G43sG9UVBWlQ",
			wantFound:     true,
		},
		{
			name:    "the hashed name is not a key",
			logical: "app-TVWDLM6W.js",
		},
		{
			name:    "the source extension is not a key",
			logical: "app.ts",
		},
		{
			name:    "unknown name resolves to the empty string, never a guess",
			logical: "nope.js",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry, found := m.Lookup(tc.logical)
			if found != tc.wantFound {
				t.Fatalf("Lookup(%q) found = %v, want %v", tc.logical, found, tc.wantFound)
			}
			if got := m.URL(tc.logical); got != tc.wantURL {
				t.Errorf("URL(%q) = %q, want %q", tc.logical, got, tc.wantURL)
			}
			if got := m.Integrity(tc.logical); got != tc.wantIntegrity {
				t.Errorf("Integrity(%q) = %q, want %q", tc.logical, got, tc.wantIntegrity)
			}
			if entry.File != tc.wantFile {
				t.Errorf("Lookup(%q).File = %q, want %q", tc.logical, entry.File, tc.wantFile)
			}
		})
	}

	if got := m.Names(); strings.Join(got, ",") != "app.css,app.js" {
		t.Errorf("Names() = %v, want [app.css app.js]", got)
	}
}

// TestManifestAddMatchesSchema pins the write side to the read side: the build
// command uses Add, every template uses URL, and a drift between them is only
// visible as a 404 in a browser.
func TestManifestAddMatchesSchema(t *testing.T) {
	body := []byte(`console.log("app");` + "\n")

	m := assets.NewManifest("assets")
	m.Add("app.js", "app-TVWDLM6W.js", body)

	if m.Base != "/assets/" {
		t.Errorf("Base = %q, want %q", m.Base, "/assets/")
	}
	entry, ok := m.Lookup("app.js")
	if !ok {
		t.Fatal("Add did not record the entry")
	}
	if entry.Path != "/assets/app-TVWDLM6W.js" {
		t.Errorf("Path = %q, want %q", entry.Path, "/assets/app-TVWDLM6W.js")
	}
	if entry.Size != int64(len(body)) {
		t.Errorf("Size = %d, want %d", entry.Size, len(body))
	}
	if want := assets.SRI(body); entry.Integrity != want {
		t.Errorf("Integrity = %q, want %q", entry.Integrity, want)
	}

	encoded, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	round, err := assets.LoadManifest(fstest.MapFS{assets.ManifestName: &fstest.MapFile{Data: encoded}})
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if round.URL("app.js") != entry.Path {
		t.Errorf("round trip URL = %q, want %q", round.URL("app.js"), entry.Path)
	}
}

func TestLoadManifestRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "a future schema is refused rather than half-read",
			body:    `{"version":99,"base":"/assets/","entries":{}}`,
			wantErr: "version 99",
		},
		{
			name:    "an unversioned file is refused",
			body:    `{"entries":{}}`,
			wantErr: "version 0",
		},
		{
			name:    "malformed json is reported as such",
			body:    `{`,
			wantErr: "parse",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := fstest.MapFS{assets.ManifestName: &fstest.MapFile{Data: []byte(tc.body)}}
			_, err := assets.LoadManifest(fsys)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}

	if _, err := assets.LoadManifest(fstest.MapFS{}); err == nil {
		t.Error("expected an error for a missing manifest")
	}
}

func TestNormalizeBase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", "/"},
		{"/", "/"},
		{"assets", "/assets/"},
		{"/assets", "/assets/"},
		{"assets/", "/assets/"},
		{"/assets/", "/assets/"},
		{"  /static/  ", "/static/"},
	}
	for _, tc := range tests {
		if got := assets.NormalizeBase(tc.in); got != tc.want {
			t.Errorf("NormalizeBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestFuncMapFailsLoudly covers the template path: a typo must stop the render
// rather than emit src="", which the browser resolves to the current page.
func TestFuncMapFailsLoudly(t *testing.T) {
	m := testManifest(t, embeddedFS(t))
	tmpl := template.Must(template.New("page").Funcs(template.FuncMap(m.FuncMap())).Parse(
		`<link rel="stylesheet" href="{{ asset .CSS }}" integrity="{{ assetIntegrity .CSS }}">`))

	var out strings.Builder
	if err := tmpl.Execute(&out, map[string]string{"CSS": "app.css"}); err != nil {
		t.Fatalf("execute with a known name: %v", err)
	}
	if !strings.Contains(out.String(), `href="/assets/app-FEPGMDJK.css"`) {
		t.Errorf("rendered %q, want the hashed href", out.String())
	}
	if !strings.Contains(out.String(), `integrity="sha384-`) {
		t.Errorf("rendered %q, want an integrity attribute", out.String())
	}

	err := tmpl.Execute(&strings.Builder{}, map[string]string{"CSS": "typo.css"})
	if err == nil {
		t.Fatal("expected execution to fail on an unknown asset")
	}
	if !strings.Contains(err.Error(), "typo.css") {
		t.Errorf("error = %q, want it to name the missing asset", err)
	}
}

// TestManifestFixtureMatchesSchema keeps the committed fixture honest about
// the shape other services parse.
func TestManifestFixtureMatchesSchema(t *testing.T) {
	raw, err := fixture.ReadFile("testdata/tree/" + assets.ManifestName)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var generic struct {
		Version int                        `json:"version"`
		Base    string                     `json:"base"`
		Entries map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	if generic.Version != assets.ManifestVersion {
		t.Errorf("fixture version = %d, want %d", generic.Version, assets.ManifestVersion)
	}
	for name, entry := range generic.Entries {
		var fields map[string]any
		if err := json.Unmarshal(entry, &fields); err != nil {
			t.Fatalf("unmarshal entry %q: %v", name, err)
		}
		for _, field := range []string{"path", "file", "integrity", "size"} {
			if _, ok := fields[field]; !ok {
				t.Errorf("entry %q is missing the %q field", name, field)
			}
		}
	}
}
