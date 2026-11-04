package appfactory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// BuildResult describes one Bazel build of an app.
type BuildResult struct {
	OK       bool          `json:"ok"`
	Log      string        `json:"log,omitempty"`
	Duration time.Duration `json:"duration"`
	Commit   string        `json:"commit,omitempty"`
}

const maxBuildLog = 8 << 10

// previewsDir holds the last successful build of every app. Bazel deletes an
// action's outputs before rerunning it, so serving bazel-bin directly would
// blank the preview whenever a change fails to build.
func (f *Factory) previewsDir() string {
	return filepath.Join(filepath.Dir(f.cfg.Workspace), ".local", "previews")
}

// PreviewDir returns the directory holding the app's last good build, or "".
func (f *Factory) PreviewDir(slug string) string {
	if !ValidSlug(slug) {
		return ""
	}
	dir := filepath.Join(f.previewsDir(), slug)
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return ""
	}
	return dir
}

// build runs `bazel build //apps/<slug>:dist` and, on success, publishes the
// output as the app's preview. Callers hold the operation lock.
func (f *Factory) build(ctx context.Context, slug string) BuildResult {
	ctx, cancel := context.WithTimeout(ctx, f.cfg.BuildTimeout)
	defer cancel()

	start := time.Now()
	out, err := f.bazel(ctx, "build", "--color=no", "--curses=no", "//apps/"+slug+":dist")
	res := BuildResult{OK: err == nil, Duration: time.Since(start).Round(time.Millisecond)}
	if err != nil {
		res.Log = tail(sanitizeLog(out, f.cfg.Workspace), maxBuildLog)
		return res
	}
	if err := f.publishPreview(ctx, slug); err != nil {
		res.OK, res.Log = false, "build succeeded but the preview could not be published: "+err.Error()
	}
	return res
}

func (f *Factory) bazel(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, f.cfg.Bazel, args...)
	cmd.Dir = f.cfg.Workspace
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// outputDir is where Bazel wrote the app's dist, asked of Bazel rather than
// read through the convenience symlink, which configuration can disable.
func (f *Factory) outputDir(ctx context.Context, slug string) (string, error) {
	if f.binDir == "" {
		cmd := exec.CommandContext(ctx, f.cfg.Bazel, "info", "bazel-bin")
		cmd.Dir = f.cfg.Workspace
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("bazel info bazel-bin: %w", err)
		}
		f.binDir = strings.TrimSpace(string(out))
	}
	return filepath.Join(f.binDir, "apps", slug, "dist"), nil
}

func (f *Factory) publishPreview(ctx context.Context, slug string) error {
	src, err := f.outputDir(ctx, slug)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(f.previewsDir(), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(f.previewsDir(), "."+slug+"-")
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.CopyFS(tmp, os.DirFS(src)); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	// Bazel outputs are read-only; previews are replaced wholesale, so make them removable.
	_ = filepath.Walk(tmp, func(p string, info os.FileInfo, err error) error {
		if err == nil {
			_ = os.Chmod(p, info.Mode().Perm()|0o200)
		}
		return nil
	})
	dst := filepath.Join(f.previewsDir(), slug)
	old := dst + ".old"
	_ = os.RemoveAll(old)
	hadOld := true
	if err := os.Rename(dst, old); errors.Is(err, os.ErrNotExist) {
		hadOld = false
	} else if err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		if hadOld {
			_ = os.Rename(old, dst)
		}
		_ = os.RemoveAll(tmp)
		return err
	}
	return os.RemoveAll(old)
}

// buildAndCommit builds the app, re-checks the boundary (a build runs the
// app's own Vite config, which could touch other files), and commits the app.
// The change is committed even when the build fails, so no edit is lost; the
// preview keeps showing the last good build. base is the commit the
// operation started from.
func (f *Factory) buildAndCommit(ctx context.Context, slug, base, message string) (BuildResult, error) {
	res := f.build(ctx, slug)
	bctx, cancel := detached(ctx)
	defer cancel()
	if _, err := f.enforceBoundary(bctx, slug, base); err != nil {
		return res, err
	}
	sha, err := f.commit(bctx, slug, message)
	if err != nil {
		return res, err
	}
	res.Commit = sha
	return res, nil
}

func sanitizeLog(s, workspace string) string {
	s = strings.ReplaceAll(s, workspace, "<workspace>")
	if home, err := os.UserHomeDir(); err == nil {
		s = strings.ReplaceAll(s, home, "~")
	}
	return s
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

/* -------------------------------------------------------------------- git -- */

// gitEnv pins every git command to the workspace repository and ignores any
// configuration outside it, so neither a parent repository nor a user or
// system gitconfig (which a prompt run could have edited) takes effect.
func (f *Factory) gitEnv() []string {
	env := []string{
		"GIT_DIR=" + filepath.Join(f.cfg.Workspace, ".git"),
		"GIT_WORK_TREE=" + f.cfg.Workspace,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	}
	for _, name := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR"} {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	return env
}

func (f *Factory) gitCmd(ctx context.Context, args ...string) (string, error) {
	out, err := f.gitRaw(ctx, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is gitCmd without trimming, for formats where leading spaces matter.
// Hooks and filesystem monitors are disabled: the agent can write .git, and
// nothing it plants may run when Timlee commits.
func (f *Factory) gitRaw(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "user.name=Timlee", "-c", "user.email=timlee@localhost",
		"-c", "commit.gpgsign=false",
	}, args...)...)
	cmd.Dir = f.cfg.Workspace
	cmd.Env = f.gitEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// ensureRepo makes the workspace its own git repository if it is not one yet
// (a fresh clone of the Timlee repository carries the workspace's files but
// not its history).
func (f *Factory) ensureRepo() error {
	if _, err := os.Stat(filepath.Join(f.cfg.Workspace, ".git")); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"commit", "-q", "--allow-empty", "-m", "Initialize Timlee app workspace"},
	} {
		if _, err := f.gitCmd(ctx, args...); err != nil {
			return err
		}
	}
	f.log.Info("appfactory: initialized workspace repository", "workspace", f.cfg.Workspace)
	return nil
}

// commit stages apps/<slug> and commits it if anything changed. It returns the
// new commit's short hash, or "" when there was nothing to commit.
func (f *Factory) commit(ctx context.Context, slug, message string) (string, error) {
	pathspec := "apps/" + slug
	if _, err := f.gitCmd(ctx, "add", "-A", "--", pathspec); err != nil {
		return "", err
	}
	if _, err := f.gitCmd(ctx, "diff", "--cached", "--quiet", "--", pathspec); err == nil {
		return "", nil
	}
	if _, err := f.gitCmd(ctx, "commit", "-q", "-m", message, "--", pathspec); err != nil {
		return "", err
	}
	return f.gitCmd(ctx, "rev-parse", "--short", "HEAD")
}

// History returns the app's recent commits, newest first.
func (f *Factory) History(ctx context.Context, slug string, limit int) ([]string, error) {
	out, err := f.gitCmd(ctx, "log", fmt.Sprintf("-n%d", limit), "--format=%h %cr %s", "--", "apps/"+slug)
	if err != nil || out == "" {
		return nil, err
	}
	return strings.Split(out, "\n"), nil
}
