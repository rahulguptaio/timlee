// Package devstack supervises the local Timlee stack: it compiles each
// service, starts them in dependency order, waits for each to report healthy,
// interleaves their logs into one readable terminal, and — the part that
// matters most — guarantees that nothing survives shutdown holding a port.
package devstack

import (
	"bytes"
	"io"
	"os"
	"strings"
	"sync"
)

// ANSI colours, by the name a Spec uses.
var ansi = map[string]string{
	"red": "31", "green": "32", "yellow": "33",
	"blue": "34", "magenta": "35", "cyan": "36", "gray": "90",
}

// Palette is the order colours are handed out to services, chosen so adjacent
// prefixes never look alike in a terminal.
var Palette = []string{"green", "cyan", "blue", "magenta", "yellow"}

// ColorEnabled decides whether to emit escape codes.
//
// NO_COLOR (https://no-color.org) wins outright; otherwise colour only when
// the destination is a terminal, because a redirected log full of escape
// codes is worse than no colour at all.
func ColorEnabled(out *os.File, env func(string) string) bool {
	if value := env("NO_COLOR"); value != "" {
		return false
	}
	if value := env("FORCE_COLOR"); value != "" && value != "0" {
		return true
	}
	info, err := out.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Paint wraps text in a colour when enabled.
func Paint(text, color string, enabled bool) string {
	if !enabled {
		return text
	}
	switch color {
	case "bold":
		return "\x1b[1m" + text + "\x1b[22m"
	case "dim":
		return "\x1b[2m" + text + "\x1b[22m"
	}
	code, ok := ansi[color]
	if !ok {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[39m"
}

// Printer serialises prefixed output from several services onto one stream.
type Printer struct {
	mu      sync.Mutex
	out     io.Writer
	width   int
	colored bool
}

// NewPrinter builds a Printer that pads every prefix to width.
func NewPrinter(out io.Writer, width int, colored bool) *Printer {
	return &Printer{out: out, width: width, colored: colored}
}

// Colored reports whether this printer emits escape codes.
func (p *Printer) Colored() bool { return p.colored }

// Line writes one prefixed line. Concurrent callers are serialised, so two
// services can never interleave within a line.
func (p *Printer) Line(name, color, text string) {
	prefix := Paint(pad(name, p.width), color, p.colored) + " " + Paint("│", "dim", p.colored) + " "

	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = io.WriteString(p.out, prefix+text+"\n")
}

// Raw writes text with no prefix, taking the same lock so it cannot land in
// the middle of a service's line.
func (p *Printer) Raw(text string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, _ = io.WriteString(p.out, text)
}

func pad(name string, width int) string {
	if len(name) >= width {
		return name
	}
	return name + strings.Repeat(" ", width-len(name))
}

// lineWriter turns an arbitrary byte stream into whole lines.
//
// A child writes whenever it likes, so a log line routinely arrives split
// across two reads. Emitting each read verbatim would scatter one line across
// several prefixes; buffering to the newline keeps the terminal readable.
type lineWriter struct {
	buf  []byte
	emit func(string)
	// max bounds the buffer, so a child that writes a megabyte without a
	// newline cannot grow it without limit.
	max int
}

func newLineWriter(emit func(string)) *lineWriter {
	return &lineWriter{emit: emit, max: 64 << 10}
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		index := bytes.IndexByte(w.buf, '\n')
		if index < 0 {
			break
		}
		w.emit(strings.TrimSuffix(string(w.buf[:index]), "\r"))
		w.buf = w.buf[index+1:]
	}
	if len(w.buf) > w.max {
		w.emit(string(w.buf))
		w.buf = w.buf[:0]
	}
	return len(p), nil
}

// Flush emits whatever is left, for a child that exited mid-line.
func (w *lineWriter) Flush() {
	if len(w.buf) > 0 {
		w.emit(string(w.buf))
		w.buf = w.buf[:0]
	}
}
