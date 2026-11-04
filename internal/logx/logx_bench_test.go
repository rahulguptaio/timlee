package logx

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The event channel sits on every request path, and until a store exists its
// sink discards. Discarded must mean free: these benchmarks pin 0 allocs/op
// for the discarded paths, which the perf-bench gate then holds.

func BenchmarkEvent(b *testing.B) {
	ctx := context.Background()
	b.Run("discarded", func(b *testing.B) {
		previous := sink.Swap(nil)
		b.Cleanup(func() { sink.Store(previous) })
		b.ReportAllocs()
		for b.Loop() {
			Event(ctx, "optimizer.result", slog.Int("tokens_saved", 3), slog.String("mode", "on"))
		}
	})
	b.Run("kept", func(b *testing.B) {
		previous := sink.Swap(&sinkBox{discardSink{}})
		b.Cleanup(func() { sink.Store(previous) })
		b.ReportAllocs()
		for b.Loop() {
			Event(ctx, "optimizer.result", slog.Int("tokens_saved", 3), slog.String("mode", "on"))
		}
	})
}

// BenchmarkRequestScope is what httpx.Logging adds to every request.
func BenchmarkRequestScope(b *testing.B) {
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	b.Run("discarded", func(b *testing.B) {
		previous := sink.Swap(nil)
		b.Cleanup(func() { sink.Store(previous) })
		b.ReportAllocs()
		for b.Loop() {
			r := Begin(request, "0123456789abcdef0123456789abcdef")
			Identify(r.Context(), IDs{UserID: "usr_1"})
			End(r, http.StatusOK, time.Millisecond)
		}
	})
	b.Run("kept", func(b *testing.B) {
		previous := sink.Swap(&sinkBox{discardSink{}})
		b.Cleanup(func() { sink.Store(previous) })
		b.ReportAllocs()
		for b.Loop() {
			r := Begin(request, "0123456789abcdef0123456789abcdef")
			Identify(r.Context(), IDs{UserID: "usr_1"})
			End(r, http.StatusOK, time.Millisecond)
		}
	})
}

// BenchmarkDisabledLevel is a debug line under an info logger from New.
func BenchmarkDisabledLevel(b *testing.B) {
	logger := New(Options{Writer: io.Discard, Level: slog.LevelInfo})
	b.ReportAllocs()
	for b.Loop() {
		logger.Debug("cache sweep", "evicted", 3)
	}
}

type discardSink struct{}

func (discardSink) Write(Record) {}
