package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestNewWritesTheLinesServicesAlwaysHave(t *testing.T) {
	var out bytes.Buffer
	logger := New(Options{Writer: &out, Level: slog.LevelInfo, Service: "timlee-test", Version: "v1", Env: "test"}).
		With("service", "timlee-test")
	logger.Info("request", "method", "GET", "status", 200)
	logger.Debug("below the level")

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one line, got %q", out.String())
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &line); err != nil {
		t.Fatalf("not JSON: %v: %s", err, lines[0])
	}
	var keys []string
	for key := range line {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	// The event channel's process attributes (version, env, instance) must
	// not leak onto operational lines.
	if want := []string{"level", "method", "msg", "service", "status", "time"}; !slices.Equal(keys, want) {
		t.Fatalf("keys %v, want %v", keys, want)
	}
}

func TestLevelAndFormatFromTheEnvironment(t *testing.T) {
	cases := []struct {
		name, level, format string
		wantDebug, wantInfo bool
		wantText, wantWarn  bool
	}{
		{name: "service default", wantInfo: true},
		{name: "quieter", level: "warn"},
		{name: "louder, any case", level: "DEBUG", wantDebug: true, wantInfo: true},
		{name: "unknown level keeps the default", level: "loud", wantInfo: true, wantWarn: true},
		{name: "text for a terminal", format: "text", wantInfo: true, wantText: true},
		{name: "unknown format stays JSON", format: "xml", wantInfo: true, wantWarn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TIMLEE_LOG_LEVEL", tc.level)
			t.Setenv("TIMLEE_LOG_FORMAT", tc.format)
			var out bytes.Buffer
			logger := New(Options{Writer: &out, Level: slog.LevelInfo})

			ctx := context.Background()
			if got := logger.Enabled(ctx, slog.LevelDebug); got != tc.wantDebug {
				t.Errorf("debug enabled = %v, want %v", got, tc.wantDebug)
			}
			if got := logger.Enabled(ctx, slog.LevelInfo); got != tc.wantInfo {
				t.Errorf("info enabled = %v, want %v", got, tc.wantInfo)
			}
			logger.Info("probe")
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if got := strings.HasPrefix(lines[len(lines)-1], "time="); got != tc.wantText {
				t.Errorf("text = %v, want %v: %s", got, tc.wantText, out.String())
			}
			if got := strings.Contains(out.String(), "logging configuration ignored"); got != tc.wantWarn {
				t.Errorf("warned = %v, want %v: %q", got, tc.wantWarn, out.String())
			}
		})
	}
}

// TestEventsAreDiscardedByDefault is the user's rule: events are captured but
// written nowhere until a store exists. The emitting half runs in a child
// process, so a write to the real stdout or stderr descriptors is seen
// however it is made.
func TestEventsAreDiscardedByDefault(t *testing.T) {
	if os.Getenv("LOGX_DISCARD_CHILD") == "1" {
		New(Options{Service: "timlee-test"})
		if Enabled() {
			t.Fatal("events must be discarded by default")
		}
		r := Begin(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), "req-discarded")
		Identify(r.Context(), IDs{UserID: "usr_discarded"})
		Fail(r, errors.New("upstream refused"))
		func() {
			defer func() { Panic(r, recover()) }()
			panic("kaboom")
		}()
		Event(r.Context(), "optimizer.result", slog.Int("tokens_saved", 12))
		End(r, http.StatusBadGateway, time.Millisecond)
		return
	}

	child := exec.Command(os.Args[0], "-test.run=^TestEventsAreDiscardedByDefault$", "-test.count=1")
	child.Env = append(os.Environ(), "LOGX_DISCARD_CHILD=1")
	out, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	for _, trace := range []string{"optimizer.result", "request.failed", "req-discarded", "usr_discarded", "upstream refused", "kaboom"} {
		if bytes.Contains(out, []byte(trace)) {
			t.Fatalf("the default configuration wrote %q:\n%s", trace, out)
		}
	}
}

func TestDiscardedEventsCostNothing(t *testing.T) {
	previous := sink.Swap(nil)
	t.Cleanup(func() { sink.Store(previous) })
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := context.Background()

	allocs := testing.AllocsPerRun(200, func() {
		Event(ctx, "optimizer.result", slog.Int("tokens_saved", 3), slog.String("mode", "on"))
		r := Begin(request, "req-1")
		Identify(r.Context(), IDs{UserID: "usr_1"})
		End(r, http.StatusInternalServerError, time.Millisecond)
	})
	if allocs != 0 {
		t.Fatalf("a discarded event allocated %v times", allocs)
	}
}
