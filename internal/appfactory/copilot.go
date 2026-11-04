package appfactory

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// MaxPromptLen bounds one prompt.
const MaxPromptLen = 8000

// StartPrompt validates the request, claims the workspace, and runs the prompt
// in the background. Progress is published on the job.
func (f *Factory) StartPrompt(slug, prompt string) (*Job, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || len([]rune(prompt)) > MaxPromptLen {
		return nil, fmt.Errorf("%w: prompts must be 1–%d characters", ErrInvalid, MaxPromptLen)
	}
	app, err := f.Get(slug)
	if err != nil {
		return nil, err
	}
	end, err := f.begin(slug)
	if err != nil {
		return nil, err
	}
	job := f.Jobs.start(slug, prompt)
	go func() {
		ok := f.runPrompt(job, app, prompt)
		// Release the workspace before announcing the end, so a client that
		// reacts to "done" can immediately start the next change.
		end()
		job.finish(ok)
	}()
	return job, nil
}

func (f *Factory) runPrompt(job *Job, app App, prompt string) bool {
	ctx := context.Background()
	head, err := f.gitCmd(ctx, "rev-parse", "HEAD")
	if err != nil {
		f.log.Error("appfactory: read HEAD", "slug", app.Slug, "err", err)
		job.emit("error", "The workspace repository is not readable.", nil)
		return false
	}
	// The agent can write .git; its config could define filters or commands
	// that git would run for Timlee later. Restore it byte for byte afterwards.
	gitConfig := filepath.Join(f.cfg.Workspace, ".git", "config")
	savedConfig, err := os.ReadFile(gitConfig)
	if err != nil {
		f.log.Error("appfactory: read git config", "err", err)
		job.emit("error", "The workspace repository is not readable.", nil)
		return false
	}

	job.emit("status", "Copilot is working on your app…", nil)
	runErr := f.runCopilot(ctx, job, app, prompt)
	if runErr != nil {
		f.log.Warn("appfactory: copilot run failed", "slug", app.Slug, "err", runErr)
		job.emit("error", userMessage(runErr), nil)
	}

	if current, err := os.ReadFile(gitConfig); err != nil || !bytes.Equal(current, savedConfig) {
		f.log.Warn("appfactory: restoring git config changed during a run", "slug", app.Slug)
		if err := os.WriteFile(gitConfig, savedConfig, 0o644); err != nil {
			job.emit("error", "The workspace repository could not be restored; the change was not committed.", nil)
			return false
		}
	}

	// Copilot runs with full tool access; whatever it did, only this app's
	// directory may change, and Timlee-owned files are regenerated.
	bctx, cancel := detached(ctx)
	restored, err := f.enforceBoundary(bctx, app.Slug, head)
	cancel()
	if err != nil {
		f.log.Error("appfactory: enforce boundary", "slug", app.Slug, "err", err)
		job.emit("error", "Could not verify which files changed; the change was not committed.", nil)
		return false
	}
	if len(restored) > 0 {
		job.emit("status", fmt.Sprintf("Reverted %d change(s) outside this app.", len(restored)), nil)
	}

	job.emit("status", "Building with Bazel…", nil)
	res, err := f.buildAndCommit(ctx, app.Slug, head, commitMessage(prompt, job.ID))
	if err != nil {
		f.log.Error("appfactory: commit", "slug", app.Slug, "err", err)
		job.emit("error", "The change could not be saved to the workspace repository.", nil)
		return false
	}
	ok := res.OK
	text := fmt.Sprintf("Build succeeded in %s.", res.Duration.Round(100*time.Millisecond))
	if !res.OK {
		text = "Build failed. The preview still shows the last working version.\n" + res.Log
	}
	job.emit("build", text, &ok)
	return runErr == nil && res.OK
}

// commitMessage records a short summary and the job, not the full prompt:
// the workspace history may be backed up or pushed, and prompts can contain
// anything the user typed.
func commitMessage(prompt, jobID string) string {
	first, _, _ := strings.Cut(prompt, "\n")
	if r := []rune(first); len(r) > 72 {
		first = string(r[:71]) + "…"
	}
	return "Prompt: " + first + "\n\nJob: " + jobID
}

// runError carries a message that is safe to show to the user.
type runError struct {
	user string
	err  error
}

func (e *runError) Error() string { return e.user + ": " + fmt.Sprint(e.err) }
func (e *runError) Unwrap() error { return e.err }

func userMessage(err error) string {
	var re *runError
	if errors.As(err, &re) {
		return re.user
	}
	return "Copilot could not finish this prompt."
}

func (f *Factory) copilotArgs(prompt string) []string {
	args := []string{
		"-p", prompt,
		"-s", "--no-color", "--no-ask-user", "--no-auto-update",
		"--output-format", "json", "--stream", "on",
		// Full tool access was chosen for app generation. Path access stays at the
		// CLI default (the app directory); enforceBoundary reverts anything else.
		"--allow-all-tools",
		"--disable-builtin-mcps",
	}
	if f.cfg.Model != "" {
		args = append(args, "--model", f.cfg.Model)
	}
	if f.cfg.MaxAICredits != "" {
		args = append(args, "--max-ai-credits", f.cfg.MaxAICredits)
	}
	return args
}

func wrapPrompt(app App, prompt string) string {
	return fmt.Sprintf(`You are editing the Timlee app %q in the current directory (apps/%s/ of the Timlee Bazel workspace).
Follow the workspace AGENTS.md exactly: change files only in this directory, use Svelte 5 and Web Awesome components themed with design tokens, and keep the app building with Bazel.

The user's request:
%s`, app.Name, app.Slug, prompt)
}

var childEnvAllow = []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "USER", "SHELL"}

func (f *Factory) runCopilot(ctx context.Context, job *Job, app App, prompt string) error {
	token, err := f.githubToken(ctx)
	if err != nil {
		return &runError{"The GitHub CLI on the Timlee server is not signed in. Run `gh auth login` there.", err}
	}
	if err := os.MkdirAll(f.cfg.CopilotHome, 0o700); err != nil {
		return &runError{"The Copilot home directory could not be created.", err}
	}
	env := []string{
		"COPILOT_HOME=" + f.cfg.CopilotHome,
		"COPILOT_GITHUB_TOKEN=" + token,
		"COPILOT_AUTO_UPDATE=false",
		"NO_COLOR=1",
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
	}
	for _, name := range childEnvAllow {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, f.cfg.RunTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, f.cfg.Copilot, f.copilotArgs(wrapPrompt(app, prompt))...)
	cmd.Dir = f.appDir(app.Slug)
	cmd.Env = env
	// Own process group, so a timeout kills every tool process Copilot spawned.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4 << 10}
	if err := cmd.Start(); err != nil {
		return &runError{"The Copilot CLI could not be started on the Timlee server.", err}
	}
	result := parseEvents(stdout, job)
	// Keep reading whatever parseEvents left (an oversized line stops its
	// scanner), so the CLI never blocks on a full pipe.
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if runCtx.Err() == context.DeadlineExceeded {
		return &runError{fmt.Sprintf("Copilot ran longer than %s and was stopped.", f.cfg.RunTimeout), runCtx.Err()}
	}
	if waitErr != nil || result.exitCode != 0 {
		return &runError{"Copilot stopped with an error.", fmt.Errorf("exit %v (result %d): %s", waitErr, result.exitCode, strings.TrimSpace(stderr.String()))}
	}
	return nil
}

func (f *Factory) githubToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.cfg.GH, "auth", "token")
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	out, err := cmd.Output()
	token := strings.TrimSpace(string(out))
	if err != nil || token == "" || strings.ContainsAny(token, " \t\r\n") {
		return "", errors.New("gh auth token failed")
	}
	return token, nil
}

type runResult struct{ exitCode int }

// parseEvents turns the CLI's JSONL stream into user-visible progress. Only
// tool titles, final assistant text, and usage are surfaced; tool arguments
// and outputs are not, since they can be large or contain paths.
func parseEvents(r io.Reader, job *Job) runResult {
	res := runResult{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var ev struct {
			Type     string          `json:"type"`
			Data     json.RawMessage `json:"data"`
			ExitCode *int            `json:"exitCode"`
			Usage    struct {
				PremiumRequests *float64 `json:"premiumRequests"`
				CodeChanges     struct {
					LinesAdded   int `json:"linesAdded"`
					LinesRemoved int `json:"linesRemoved"`
				} `json:"codeChanges"`
			} `json:"usage"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		var data struct {
			ToolName     string            `json:"toolName"`
			ToolTitle    string            `json:"toolTitle"`
			Content      string            `json:"content"`
			Message      string            `json:"message"`
			ToolRequests []json.RawMessage `json:"toolRequests"`
		}
		_ = json.Unmarshal(ev.Data, &data)
		switch {
		case ev.Type == "tool.execution_start":
			title := firstNonEmpty(data.ToolTitle, data.ToolName)
			if title != "" {
				job.emit("tool", truncate(title, 200), nil)
			}
		case ev.Type == "assistant.message" && strings.TrimSpace(data.Content) != "":
			job.emit("message", truncate(data.Content, 8000), nil)
		case ev.Type == "result":
			if ev.ExitCode != nil {
				res.exitCode = *ev.ExitCode
			}
			text := fmt.Sprintf("+%d / -%d lines", ev.Usage.CodeChanges.LinesAdded, ev.Usage.CodeChanges.LinesRemoved)
			if ev.Usage.PremiumRequests != nil {
				text += fmt.Sprintf(", %g premium request(s)", *ev.Usage.PremiumRequests)
			}
			job.emit("usage", text, nil)
		case ev.Type == "error" || strings.HasSuffix(ev.Type, ".error"):
			job.emit("error", truncate(firstLine(firstNonEmpty(data.Message, "Copilot reported an error.")), 300), nil)
		}
	}
	return res
}

// enforceBoundary undoes everything changed outside apps/<slug>/ since base,
// including commits the agent may have made itself, and regenerates the
// Timlee-owned files from the committed timlee.json. Callers hold the
// operation lock, so any commit after base was made during this operation.
// It returns the reverted paths.
func (f *Factory) enforceBoundary(ctx context.Context, slug, base string) ([]string, error) {
	if head, err := f.gitCmd(ctx, "rev-parse", "HEAD"); err != nil {
		return nil, err
	} else if head != base {
		// Fold the agent's own commits back into the working tree.
		if _, err := f.gitCmd(ctx, "reset", "-q", "--soft", base); err != nil {
			return nil, err
		}
	}
	if _, err := f.gitCmd(ctx, "reset", "-q", "--", "."); err != nil {
		return nil, err
	}
	out, err := f.gitRaw(ctx, "status", "--porcelain=v1", "--untracked-files=all", "--no-renames", "-z")
	if err != nil {
		return nil, err
	}
	own := "apps/" + slug + "/"
	var restored []string
	for _, entry := range strings.Split(out, "\x00") {
		if len(entry) < 4 {
			continue
		}
		code, p := entry[:2], entry[3:]
		if strings.HasPrefix(p, own) {
			continue
		}
		if code == "??" {
			if err := os.RemoveAll(filepath.Join(f.cfg.Workspace, filepath.FromSlash(p))); err != nil {
				return restored, err
			}
		} else if _, err := f.gitCmd(ctx, "checkout", "-q", base, "--", p); err != nil {
			return restored, err
		}
		restored = append(restored, p)
	}

	// timlee.json is Timlee's; the theme files are regenerated from it.
	if _, err := f.gitCmd(ctx, "cat-file", "-e", base+":"+own+"timlee.json"); err == nil {
		if err := f.writeAppFile(slug, "timlee.json", nil); err != nil {
			return restored, err
		}
		if _, err := f.gitCmd(ctx, "checkout", "-q", base, "--", own+"timlee.json"); err != nil {
			return restored, err
		}
	}
	app, err := f.Get(slug)
	if err != nil {
		return restored, err
	}
	return restored, f.writeThemeFiles(app)
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
