// Package locales carries the message catalogs compiled into the binary:
// one directory per locale ID, one ARB file per surface, as docs/I18N.md §3
// lays out. internal/i18n.Load reads them.
//
// //go:embed cannot reach outside its own package directory, so the embed
// lives beside the catalogs, like web/templates.
package locales

import "embed"

//go:embed */*.arb
var files embed.FS

// FS returns the embedded catalogs, interchangeable with
// os.DirFS("web/locales").
func FS() embed.FS { return files }

// Dir is where the same tree lives on disk, relative to the repository root.
const Dir = "web/locales"
