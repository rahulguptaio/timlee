package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/rahulguptaio/timlee/internal/i18n"
)

var clientMessageBlock = regexp.MustCompile(
	`<script type="application/json" id="timlee-i18n"[^>]*>([^<]*)</script>`,
)

var clientMessageCall = regexp.MustCompile(`\b(?:t|tn)\(\s*['"]([^'"]+)['"]`)

func TestClientKeyContractsMatchTypeScript(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		keys  []i18n.Key
	}{
		{
			name:  "components",
			paths: productionTypeScript(t, "../../web/components"),
			keys:  componentClientKeys,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			typescript := []string{}
			for _, path := range tc.paths {
				src, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				for _, match := range clientMessageCall.FindAllSubmatch(src, -1) {
					typescript = append(typescript, string(match[1]))
				}
			}
			slices.Sort(typescript)
			typescript = slices.Compact(typescript)

			server := make([]string, len(tc.keys))
			for i, key := range tc.keys {
				server[i] = string(key)
			}
			slices.Sort(server)
			server = slices.Compact(server)
			if got, want := strings.Join(server, "\n"), strings.Join(typescript, "\n"); got != want {
				t.Errorf("server keys differ from TypeScript:\nserver:\n%s\n\nTypeScript:\n%s", got, want)
			}
		})
	}
}

func productionTypeScript(t *testing.T, root string) []string {
	t.Helper()
	paths := []string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".ts" {
			return nil
		}
		if strings.Contains(path, ".test.") ||
			strings.HasSuffix(path, "i18n-fixture.ts") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

func clientBlockOf(t *testing.T, body string) struct {
	Lang string           `json:"lang"`
	Dir  string           `json:"dir"`
	M    map[string][]any `json:"m"`
} {
	t.Helper()
	match := clientMessageBlock.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("page has no client message block")
	}
	var block struct {
		Lang string           `json:"lang"`
		Dir  string           `json:"dir"`
		M    map[string][]any `json:"m"`
	}
	if err := json.Unmarshal([]byte(match[1]), &block); err != nil {
		t.Fatalf("decode client message block: %v", err)
	}
	return block
}

func TestLandingSendsPseudoRTLComponentMessages(t *testing.T) {
	h := localeHarness(t, pseudoLocales(t))
	block := clientBlockOf(t, h.get("/ar-xb/").Body.String())

	if block.Lang != "ar-XB" || block.Dir != "rtl" {
		t.Fatalf("client locale = %q %q, want ar-XB rtl", block.Lang, block.Dir)
	}
	for _, key := range []string{
		"components.toast.dismiss",
		"components.dialog.close",
	} {
		if _, ok := block.M[key]; !ok {
			t.Errorf("landing client block is missing %q", key)
		}
	}
}
