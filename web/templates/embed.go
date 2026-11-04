// Package templates carries the server-rendered HTML compiled into the
// binary.
//
// //go:embed cannot reach outside its own package directory, so the embed
// lives beside the templates rather than in internal/render. A release binary
// is then the only artefact that ships; development re-reads the same tree
// from disk through FS, so a template edit is visible on the next request
// instead of the next `go build`.
package templates

import "embed"

//go:embed layouts/*.gohtml partials/*.gohtml pages/*.gohtml
var files embed.FS

// FS returns the embedded template tree, interchangeable with
// os.DirFS("web/templates").
func FS() embed.FS { return files }

// Dir is where the same tree lives on disk, relative to the repository root.
const Dir = "web/templates"
