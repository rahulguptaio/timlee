package assets_test

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/rahulguptaio/timlee/internal/assets"
	"github.com/rahulguptaio/timlee/internal/assets/embedded"
)

// The same fixture tree is reached three ways so the tests can prove the
// handler does not care which one it was given: compiled into the binary,
// read from disk, and held in memory.
//
//go:embed testdata/tree
var fixture embed.FS

// embeddedFS is the release shape: an embed.FS, whose files report a zero
// modification time.
func embeddedFS(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(fixture, "testdata/tree")
	if err != nil {
		t.Fatalf("sub embedded fixture: %v", err)
	}
	return sub
}

// diskFS is the development shape: a real directory, whose files carry real
// modification times.
func diskFS(t *testing.T) fs.FS {
	t.Helper()
	dir := t.TempDir()
	src := embeddedFS(t)
	err := fs.WalkDir(src, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(src, name)
		if err != nil {
			return err
		}
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return os.WriteFile(full, body, 0o644)
	})
	if err != nil {
		t.Fatalf("materialise fixture on disk: %v", err)
	}
	return assets.Dir(dir)
}

// memoryFS stands for any fs.FS that is neither: its files do not implement
// io.Seeker, which is the path http.ServeContent cannot take directly.
func memoryFS(t *testing.T) fs.FS {
	t.Helper()
	src := embeddedFS(t)
	m := fstest.MapFS{}
	err := fs.WalkDir(src, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(src, name)
		if err != nil {
			return err
		}
		m[name] = &fstest.MapFile{Data: body}
		return nil
	})
	if err != nil {
		t.Fatalf("materialise fixture in memory: %v", err)
	}
	return unseekable{m}
}

type unseekable struct{ fs.FS }

func (u unseekable) Open(name string) (fs.File, error) {
	f, err := u.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return readOnlyFile{f}, nil
}

// readOnlyFile hides the embedded file's Seek method from a type assertion.
type readOnlyFile struct{ inner fs.File }

func (f readOnlyFile) Stat() (fs.FileInfo, error) { return f.inner.Stat() }
func (f readOnlyFile) Read(p []byte) (int, error) { return f.inner.Read(p) }
func (f readOnlyFile) Close() error               { return f.inner.Close() }

// sources are every fs.FS shape the handler must behave identically over.
func sources(t *testing.T) map[string]fs.FS {
	t.Helper()
	return map[string]fs.FS{
		"embedded": embeddedFS(t),
		"disk":     diskFS(t),
		"memory":   memoryFS(t),
	}
}

func testManifest(t *testing.T, fsys fs.FS) *assets.Manifest {
	t.Helper()
	m, err := assets.LoadManifest(fsys)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	return m
}

// TestEmbeddedFSIsUsable guards the release path: the generated package must
// produce an FS even before any build has populated it, or `go build ./...`
// would depend on having run the asset build first.
func TestEmbeddedFSIsUsable(t *testing.T) {
	fsys, err := embedded.FS()
	if err != nil {
		t.Fatalf("embedded.FS(): %v", err)
	}
	if _, err := fs.ReadDir(fsys, "."); err != nil {
		t.Fatalf("read embedded asset root: %v", err)
	}
}
