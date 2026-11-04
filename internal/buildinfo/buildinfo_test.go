package buildinfo

import (
	"errors"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
)

const (
	rev     = "7896b1a1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7"
	revTime = "2026-10-29T04:42:37Z"
)

func checkout(mainVersion string, modified bool) *debug.BuildInfo {
	dirty := "false"
	if modified {
		dirty = "true"
	}
	return &debug.BuildInfo{
		Main: debug.Module{Path: "github.com/rahulguptaio/timlee", Version: mainVersion},
		Settings: []debug.BuildSetting{
			{Key: "CGO_ENABLED", Value: "0"},
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: rev},
			{Key: "vcs.time", Value: revTime},
			{Key: "vcs.modified", Value: dirty},
		},
	}
}

// stamp stands in for -ldflags -X for the length of one test.
func stamp(t *testing.T, version, commit, date string) {
	t.Helper()
	old := [3]string{Version, Commit, Date}
	Version, Commit, Date = version, commit, date
	t.Cleanup(func() { Version, Commit, Date = old[0], old[1], old[2] })
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name                 string
		version, commit, day string
		bi                   *debug.BuildInfo
		want                 Info
	}{
		{
			name: "unstamped build from a dirty checkout falls back to the VCS state",
			bi:   checkout("(devel)", true),
			want: Info{Version: "dev", Commit: rev, Date: revTime, Modified: true},
		},
		{
			name: "unstamped build uses the module pseudo-version when there is one",
			bi:   checkout("v0.0.0-20261029044237-7896b1a1c2d3", false),
			want: Info{Version: "v0.0.0-20261029044237-7896b1a1c2d3", Commit: rev, Date: revTime},
		},
		{
			name:    "the linker stamp wins over the VCS state, which still reports dirt",
			version: "1.4.0", commit: "0123456789abcdef0123456789abcdef01234567", day: "2026-11-01T00:00:00Z",
			bi:   checkout("(devel)", true),
			want: Info{Version: "1.4.0", Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-11-01T00:00:00Z", Modified: true},
		},
		{
			name: "no build information at all",
			bi:   nil,
			want: Info{Version: "dev", Commit: "unknown", Date: "unknown"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stamp(t, tt.version, tt.commit, tt.day)
			got := resolve(tt.bi)
			got.Go, got.Platform = "", ""
			if got != tt.want {
				t.Errorf("resolve() = %+v\nwant          %+v", got, tt.want)
			}
		})
	}
}

func TestStringMarksAModifiedTree(t *testing.T) {
	info := Info{Version: "1.4.0", Commit: rev, Date: revTime, Go: "go1.27.3", Platform: "linux/amd64"}
	clean := "1.4.0 (commit " + rev + ", built " + revTime + ", go1.27.3 linux/amd64)"
	if got := info.String(); got != clean {
		t.Errorf("clean:\n got %q\nwant %q", got, clean)
	}
	info.Modified = true
	dirty := "1.4.0 (commit " + rev + ", modified, built " + revTime + ", go1.27.3 linux/amd64)"
	if got := info.String(); got != dirty {
		t.Errorf("modified:\n got %q\nwant %q", got, dirty)
	}
}

func TestWantsVersion(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--version"}, true},
		{[]string{"-version"}, true},
		{[]string{"-dev", "-addr", ":0", "--version"}, true},
		{[]string{"--", "--version"}, false},
		{[]string{"-v"}, false},
		{[]string{"version"}, false},
		{[]string{"--versions"}, false},
		{[]string{"-version=true"}, false},
	}
	for _, tt := range tests {
		if got := wantsVersion(tt.args); got != tt.want {
			t.Errorf("wantsVersion(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}

// TestHandleVersionFlag runs HandleVersionFlag in a child copy of this test
// binary, because the behaviour under test is that it ends the process.
func TestHandleVersionFlag(t *testing.T) {
	if arg, ok := os.LookupEnv("BUILDINFO_CHILD_ARG"); ok {
		os.Args = []string{"timlee-test", "-addr", ":0", arg}
		HandleVersionFlag("timlee-test")
		os.Exit(3) // the flag was not handled
	}

	for _, tt := range []struct {
		arg      string
		wantCode int
	}{
		{"--version", 0},
		{"-version", 0},
		{"-dev", 3},
	} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHandleVersionFlag$")
		cmd.Env = append(os.Environ(), "BUILDINFO_CHILD_ARG="+tt.arg)
		out, err := cmd.Output()

		code := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: %v", tt.arg, err)
		}
		if code != tt.wantCode {
			t.Errorf("%s: exit code %d, want %d; stdout %q", tt.arg, code, tt.wantCode, out)
			continue
		}
		if tt.wantCode == 0 {
			line := string(out)
			if !strings.HasPrefix(line, "timlee-test ") || !strings.Contains(line, "(commit ") ||
				strings.Count(line, "\n") != 1 {
				t.Errorf("%s: stdout %q, want one line \"timlee-test <version> (commit …)\"", tt.arg, line)
			}
		}
	}
}
