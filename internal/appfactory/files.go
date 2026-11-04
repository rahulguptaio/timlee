package appfactory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

// MaxFileSize bounds files the editor can open or save.
const MaxFileSize = 512 << 10

// generatedFiles are owned by the theme editor; saving them directly would be
// overwritten on the next theme change.
var generatedFiles = map[string]bool{
	"timlee.json":            true,
	"src/theme.override.css": true,
	"src/font.js":            true,
}

// IsGenerated reports whether rel is regenerated from timlee.json.
func IsGenerated(rel string) bool { return generatedFiles[rel] }

// FileEntry is one file shown in the editor's tree.
type FileEntry struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Generated bool   `json:"generated"`
}

var skipDirs = map[string]bool{"dist": true, "node_modules": true, ".git": true}

// cleanRel validates an editor path: relative, slash-separated, no dot
// segments, no hidden components. os.Root additionally blocks symlink escapes.
func cleanRel(rel string) (string, error) {
	if rel == "" || strings.Contains(rel, "\\") || strings.ContainsRune(rel, 0) || path.IsAbs(rel) {
		return "", fmt.Errorf("%w: bad path", ErrInvalid)
	}
	clean := path.Clean(rel)
	if clean != rel || clean == "." {
		return "", fmt.Errorf("%w: bad path", ErrInvalid)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." || strings.HasPrefix(part, ".") || skipDirs[part] {
			return "", fmt.Errorf("%w: bad path", ErrInvalid)
		}
	}
	return clean, nil
}

func (f *Factory) openRoot(slug string) (*os.Root, error) {
	if !ValidSlug(slug) {
		return nil, ErrNotFound
	}
	root, err := os.OpenRoot(f.appDir(slug))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return root, err
}

// ListFiles returns the app's editable source files.
func (f *Factory) ListFiles(slug string) ([]FileEntry, error) {
	root, err := f.openRoot(slug)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var files []FileEntry
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, FileEntry{Path: p, Size: info.Size(), Generated: IsGenerated(p)})
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

// ReadFile returns a text file's contents.
func (f *Factory) ReadFile(slug, rel string) (string, error) {
	rel, err := cleanRel(rel)
	if err != nil {
		return "", err
	}
	root, err := f.openRoot(slug)
	if err != nil {
		return "", err
	}
	defer root.Close()
	info, err := root.Stat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxFileSize {
		return "", fmt.Errorf("%w: not an editable text file", ErrInvalid)
	}
	raw, err := root.ReadFile(rel)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(raw) {
		return "", fmt.Errorf("%w: binary files cannot be edited", ErrInvalid)
	}
	return string(raw), nil
}

// SaveFile writes one source file from the editor, rebuilds, and commits.
// New files are allowed (parent directories are created); generated files are not.
func (f *Factory) SaveFile(ctx context.Context, slug, rel, content string) (BuildResult, error) {
	rel, err := cleanRel(rel)
	if err != nil {
		return BuildResult{}, err
	}
	if IsGenerated(rel) {
		return BuildResult{}, ErrGenerated
	}
	if len(content) > MaxFileSize || !utf8.ValidString(content) {
		return BuildResult{}, fmt.Errorf("%w: files must be UTF-8 text under %d KB", ErrInvalid, MaxFileSize>>10)
	}
	end, err := f.begin(slug)
	if err != nil {
		return BuildResult{}, err
	}
	defer end()
	base, err := f.gitCmd(ctx, "rev-parse", "HEAD")
	if err != nil {
		return BuildResult{}, err
	}
	root, err := f.openRoot(slug)
	if err != nil {
		return BuildResult{}, err
	}
	defer root.Close()
	if dir := path.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return BuildResult{}, err
		}
	}
	if err := root.WriteFile(rel, []byte(content), 0o644); err != nil {
		return BuildResult{}, err
	}
	return f.buildAndCommit(ctx, slug, base, "Edit "+rel)
}
