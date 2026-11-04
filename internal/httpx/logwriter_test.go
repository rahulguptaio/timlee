package httpx_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rahulguptaio/timlee/internal/httpx"
)

// syncBuffer records each Write the LogWriter makes downstream.
type syncBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	writes  int
	partial int // writes that ended mid-line
	fail    bool
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writes++
	if !bytes.HasSuffix(p, []byte("\n")) {
		b.partial++
	}
	if b.fail {
		return 0, errors.New("disk full")
	}
	return b.buf.Write(p)
}

func (b *syncBuffer) snapshot() (string, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String(), b.writes
}

func TestLogWriterBatchesInfoLines(t *testing.T) {
	out := &syncBuffer{}
	w := httpx.NewLogWriter(out)
	logger := slog.New(slog.NewJSONHandler(w, nil))

	for i := range 100 {
		logger.Info("request", "i", i)
	}
	if got, writes := out.snapshot(); writes != 0 || got != "" {
		t.Fatalf("100 info lines reached the writer at once in %d writes; want them batched", writes)
	}

	// The background flush delivers them without being asked.
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, writes := out.snapshot()
		if strings.Count(got, "\n") == 100 {
			if writes > 2 {
				t.Errorf("100 lines took %d writes; want one batch", writes)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of 100 lines flushed within 2s", strings.Count(got, "\n"))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// A warning or an error must not wait: the next statement may be os.Exit.
func TestLogWriterWritesWarningsAndErrorsThrough(t *testing.T) {
	for _, level := range []slog.Level{slog.LevelWarn, slog.LevelError, slog.LevelError + 2} {
		for _, format := range []string{"json", "text"} {
			out := &syncBuffer{}
			w := httpx.NewLogWriter(out)
			handler := slog.Handler(slog.NewJSONHandler(w, nil))
			if format == "text" {
				handler = slog.NewTextHandler(w, nil)
			}
			logger := slog.New(handler)

			logger.Info("before")
			logger.Log(t.Context(), level, "fatal-ish", "error", "boom")

			got, _ := out.snapshot()
			if !strings.Contains(got, "before") || !strings.Contains(got, "fatal-ish") {
				t.Errorf("%s %s: written at once = %q, want the earlier line and the %s", format, level, got, level)
			}
			if strings.Index(got, "before") > strings.Index(got, "fatal-ish") {
				t.Errorf("%s %s: lines out of order: %q", format, level, got)
			}
			_ = w.Close()
		}
	}
}

func TestLogWriterCloseFlushesAndWritesThroughAfterwards(t *testing.T) {
	out := &syncBuffer{}
	w := httpx.NewLogWriter(out)
	logger := slog.New(slog.NewJSONHandler(w, nil))

	logger.Info("stopped")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got, _ := out.snapshot(); !strings.Contains(got, "stopped") {
		t.Fatalf("Close left %q unwritten", "stopped")
	}

	logger.Info("straggler")
	if got, _ := out.snapshot(); !strings.Contains(got, "straggler") {
		t.Fatal("a line logged after Close was buffered with nothing left to flush it")
	}
}

// Concurrent loggers share one writer; every line must reach it whole, and a
// full buffer must be flushed between lines, never inside one.
func TestLogWriterKeepsLinesWhole(t *testing.T) {
	out := &syncBuffer{}
	w := httpx.NewLogWriter(out)
	logger := slog.New(slog.NewJSONHandler(w, nil))

	payload := strings.Repeat("x", 700)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 500 {
				logger.Info("request", "g", g, "i", i, "pad", payload)
			}
		})
	}
	wg.Wait()
	// One line larger than the whole buffer goes straight through.
	logger.Info("huge", "pad", strings.Repeat("y", 100<<10))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	got, writes := out.snapshot()
	if out.partial != 0 {
		t.Errorf("%d of %d writes ended mid-line; another writer on the descriptor could split a record", out.partial, writes)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 8*500+1 {
		t.Fatalf("got %d lines, want %d", len(lines), 8*500+1)
	}
	for i, line := range lines {
		if !strings.HasPrefix(line, `{"time":`) || !strings.HasSuffix(line, `"}`) {
			t.Fatalf("line %d is not a whole record: %.80q…", i, line)
		}
	}
	if writes > len(lines)/10 {
		t.Errorf("%d lines took %d writes; want them batched", len(lines), writes)
	}
}

// A failed write loses that batch, as an unbuffered logger loses the line
// whose write failed, but logging carries on afterwards.
func TestLogWriterRecoversFromAFailedWrite(t *testing.T) {
	out := &syncBuffer{fail: true}
	w := httpx.NewLogWriter(out)
	logger := slog.New(slog.NewJSONHandler(w, nil))

	logger.Error("lost")
	out.mu.Lock()
	out.fail = false
	out.mu.Unlock()
	for i := range 3 {
		logger.Error(fmt.Sprintf("kept-%d", i))
	}
	_ = w.Close()

	got, _ := out.snapshot()
	if strings.Contains(got, "lost") || strings.Count(got, "kept-") != 3 {
		t.Fatalf("after a failed write the log holds %q; want the three later lines", got)
	}
}
