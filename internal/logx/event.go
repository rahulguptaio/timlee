package logx

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Record is one event as a Sink receives it. Attrs start with the process
// (service, version, commit, env, instance), then the ids of the request the
// event belongs to, then the event's own attributes, all already redacted.
type Record struct {
	Time  time.Time
	Name  string
	Attrs []slog.Attr
}

// Attr returns the value of the top-level attribute key.
func (r Record) Attr(key string) (slog.Value, bool) {
	for _, attr := range r.Attrs {
		if attr.Key == key {
			return attr.Value, true
		}
	}
	return slog.Value{}, false
}

// MarshalJSON renders the record as one JSON object, the event's name under
// "event": the line a file or database sink would store.
func (r Record) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) == 0 {
				switch attr.Key {
				case slog.LevelKey:
					return slog.Attr{}
				case slog.MessageKey:
					attr.Key = "event"
				}
			}
			return attr
		},
	})
	record := slog.NewRecord(r.Time, slog.LevelInfo, r.Name, 0)
	record.AddAttrs(r.Attrs...)
	if err := handler.Handle(context.Background(), record); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// Sink receives events. Write runs on the request path, so a sink that does
// I/O must queue rather than block, and it must be safe for concurrent use.
type Sink interface {
	Write(Record)
}

type sinkBox struct{ Sink }

// sink is where events go; nil discards them.
var sink atomic.Pointer[sinkBox]

// SetSink sends every event from now on to s. Nil restores the default,
// which discards them.
func SetSink(s Sink) {
	if s == nil {
		sink.Store(nil)
		return
	}
	sink.Store(&sinkBox{s})
}

// Enabled reports whether events are being kept. Code that does extra work to
// describe an event checks it first, so a discarded event costs nothing.
func Enabled() bool { return sink.Load() != nil }

// Event records that something named name happened. The name is a stable
// identifier catalogued in docs/LOGGING.md; attrs carry counts, codes and
// opaque ids, never text a person wrote. The ids of the request ctx belongs
// to are added unless attrs name them; secrets and email addresses are
// redacted.
func Event(ctx context.Context, name string, attrs ...slog.Attr) {
	box := sink.Load()
	if box == nil {
		return
	}
	box.Write(Record{Time: time.Now(), Name: name, Attrs: compose(ctx, attrs)})
}

func compose(ctx context.Context, attrs []slog.Attr) []slog.Attr {
	var base []slog.Attr
	if p := process.Load(); p != nil {
		base = *p
	}
	scoped := scopeOf(ctx).attrs()

	out := make([]slog.Attr, 0, len(base)+len(scoped)+len(attrs))
	out = append(out, base...)
	for _, attr := range scoped {
		if !slices.ContainsFunc(attrs, func(a slog.Attr) bool { return a.Key == attr.Key }) {
			out = append(out, redact(attr))
		}
	}
	for _, attr := range attrs {
		if attr.Key != "" {
			out = append(out, redact(attr))
		}
	}
	return out
}

// Memory keeps events in memory, for tests.
type Memory struct {
	mu      sync.Mutex
	records []Record
}

// Write keeps r.
func (m *Memory) Write(r Record) {
	m.mu.Lock()
	m.records = append(m.records, r)
	m.mu.Unlock()
}

// Records returns the events kept so far, oldest first; given names, only
// events with one of them.
func (m *Memory) Records(names ...string) []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Record
	for _, r := range m.records {
		if len(names) == 0 || slices.Contains(names, r.Name) {
			out = append(out, r)
		}
	}
	return out
}

// Capture keeps every event in a Memory until the test ends, then restores
// the sink it replaced. The sink is process-wide, so a test that captures
// must not run in parallel with others whose events it counts.
func Capture(tb interface{ Cleanup(func()) }) *Memory {
	memory := &Memory{}
	previous := sink.Swap(&sinkBox{memory})
	tb.Cleanup(func() { sink.Store(previous) })
	return memory
}
