// Package logx is how Timlee's services log. It keeps two channels apart:
//
//   - The operational log is what an operator reads: startup, shutdown, one
//     line per request, warnings and errors. New builds it, JSON to stdout
//     (journald in production), with the same fields each service has always
//     written.
//
//   - The event channel records what happened for the product: what the
//     prompt optimizer saved, what users did, which requests failed. Event
//     hands a named, redacted record that identifies people by opaque ids
//     only to a Sink. Until Timlee has somewhere to store events the sink
//     discards them, so nothing is written to stdout, stderr, a file, a
//     database or the network. SetSink plugs a real sink in at startup.
//
// docs/LOGGING.md catalogues the events and what each may carry.
package logx

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
)

// Options configures New.
type Options struct {
	// Level is the operational log's level unless TIMLEE_LOG_LEVEL names
	// another. Nil means info.
	Level slog.Leveler
	// Writer receives the operational log; nil means stdout.
	Writer io.Writer

	// Service, Version, Commit and Env describe this process on every event,
	// together with the host name. Operational lines carry only the
	// attributes each main adds itself, as they always have.
	Service, Version, Commit, Env string
}

// New returns the operational logger and records which process every event
// comes from.
//
// The logger writes JSON: the load harness and journald tooling read it line
// by line. TIMLEE_LOG_FORMAT=text switches to slog's text format for a person
// at a terminal; TIMLEE_LOG_LEVEL (debug, info, warn or error) overrides the
// level the service chose. A value New cannot use is reported on the log it
// returns and ignored.
func New(opts Options) *slog.Logger {
	out := opts.Writer
	if out == nil {
		out = os.Stdout
	}

	var problems []string
	var level slog.Leveler = slog.LevelInfo
	if opts.Level != nil {
		level = opts.Level
	}
	if value := os.Getenv("TIMLEE_LOG_LEVEL"); value != "" {
		if parsed, err := parseLevel(value); err == nil {
			level = parsed
		} else {
			problems = append(problems, err.Error())
		}
	}

	handlerOptions := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch format := os.Getenv("TIMLEE_LOG_FORMAT"); strings.ToLower(format) {
	case "", "json":
		handler = slog.NewJSONHandler(out, handlerOptions)
	case "text":
		handler = slog.NewTextHandler(out, handlerOptions)
	default:
		problems = append(problems, fmt.Sprintf("TIMLEE_LOG_FORMAT %q is not json or text", format))
		handler = slog.NewJSONHandler(out, handlerOptions)
	}

	describeProcess(opts)

	logger := slog.New(handler)
	for _, problem := range problems {
		logger.Warn("logging configuration ignored", "reason", problem)
	}
	return logger
}

// parseLevel reads debug, info, warn or error in any case, and slog's own
// offsets such as "warn+2".
func parseLevel(value string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(value)); err != nil {
		return 0, fmt.Errorf("TIMLEE_LOG_LEVEL %q is not one of debug, info, warn, error", value)
	}
	return level, nil
}

// process holds the attributes every event starts with.
var process atomic.Pointer[[]slog.Attr]

func describeProcess(opts Options) {
	host, _ := os.Hostname()
	var attrs []slog.Attr
	for _, attr := range []slog.Attr{
		slog.String("service", opts.Service),
		slog.String("version", opts.Version),
		slog.String("commit", opts.Commit),
		slog.String("env", opts.Env),
		slog.String("instance", host),
	} {
		if attr.Value.String() != "" {
			attrs = append(attrs, attr)
		}
	}
	process.Store(&attrs)
}
