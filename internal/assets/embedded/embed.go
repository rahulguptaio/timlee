// Package embedded carries the built frontend assets compiled into the binary.
//
// //go:embed can only reach files at or below its own package directory, so a
// the default production build mirrors cmd/timlee-build's output into
// ./static here:
//
//	go run ./cmd/timlee-build
//
// Custom output builds remain disk-only unless they pass -embed explicitly.
//
// The output itself is generated and not committed; static/.gitignore keeps
// the directory present in a fresh clone so this package compiles — and so
// `go build ./...` stays green — before any asset build has run.
package embedded

import (
	"embed"
	"io/fs"
)

//go:embed all:static
var files embed.FS

// FS returns the built asset tree rooted at the output directory, making it
// interchangeable with assets.Dir("web/dist").
func FS() (fs.FS, error) { return fs.Sub(files, "static") }
