package httpx

import (
	"bytes"
	"io"
	"sync"
	"time"
)

// LogWriter batches what a process logs, so recording a request costs a copy
// into memory instead of a write system call — made while every other
// request waits on the logger's lock to make its own. Under the load harness
// that call was a quarter of the data plane's CPU and most of its lock
// contention.
//
// Batched lines reach the underlying writer when the buffer fills, and at
// most logFlushInterval after the first line of a batch was logged. Warnings
// and errors from log/slog's JSON or text handlers are written through at
// once, together with everything logged before them, so the line a process
// logs just before os.Exit is not left in a buffer the exit discards. What a
// hard crash can lose is the last interval's info lines; the crash's own
// trace goes to stderr unbuffered.
//
// Give every logger in the process the same LogWriter: it writes whole lines
// only, and a second writer sharing the descriptor could otherwise land in
// the middle of a batch. Close it when the server returns.
type LogWriter struct {
	mu     sync.Mutex
	out    io.Writer
	buf    []byte
	closed bool
	// timer flushes a batch logFlushInterval after its first line, so an
	// idle process is never woken just to find nothing to write.
	timer *time.Timer
}

const (
	// logBufferSize holds a few hundred request lines.
	logBufferSize = 64 << 10
	// logFlushInterval bounds how stale a tailed log can be.
	logFlushInterval = 100 * time.Millisecond
)

// NewLogWriter starts batching writes to out.
func NewLogWriter(out io.Writer) *LogWriter {
	l := &LogWriter{out: out, buf: make([]byte, 0, logBufferSize)}
	l.timer = time.AfterFunc(time.Hour, func() { _ = l.Flush() })
	l.timer.Stop()
	return l
}

// Write buffers one line. log/slog hands over each record in a single Write,
// which is what keeps a batch from ever ending mid-line.
func (l *LogWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return l.out.Write(p)
	}
	if len(l.buf)+len(p) > logBufferSize {
		if err := l.flushLocked(); err != nil {
			return 0, err
		}
		if len(p) > logBufferSize {
			return l.out.Write(p)
		}
	}
	if len(l.buf) == 0 {
		l.timer.Reset(logFlushInterval)
	}
	l.buf = append(l.buf, p...)
	if urgent(p) {
		if err := l.flushLocked(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush writes what is buffered.
func (l *LogWriter) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.flushLocked()
}

// Close writes what is buffered. Anything logged afterwards is written
// through unbuffered.
func (l *LogWriter) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	l.timer.Stop()
	return l.flushLocked()
}

// flushLocked drops the batch even when the write fails, as an unbuffered
// logger loses the line whose write failed rather than retrying it forever.
func (l *LogWriter) flushLocked() error {
	if len(l.buf) == 0 {
		return nil
	}
	_, err := l.out.Write(l.buf)
	l.buf = l.buf[:0]
	return err
}

// urgentLevels mark a warning or an error in log/slog's JSON and text
// formats; a custom level such as WARN+2 starts the same way. The level
// follows the timestamp, well inside the first urgentWindow bytes.
var urgentLevels = [][]byte{
	[]byte(`"level":"WARN`), []byte(`"level":"ERROR`),
	[]byte(`level=WARN`), []byte(`level=ERROR`),
}

const urgentWindow = 128

func urgent(line []byte) bool {
	head := line[:min(len(line), urgentWindow)]
	for _, level := range urgentLevels {
		if bytes.Contains(head, level) {
			return true
		}
	}
	return false
}
