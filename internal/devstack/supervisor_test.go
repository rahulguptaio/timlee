package devstack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// These tests use shell commands rather than the real services, so they are
// fast and depend on nothing being built. What is under test is the
// supervisor, not what it supervises.

func quietPrinter() *Printer { return NewPrinter(io.Discard, 6, false) }

// shellSpec builds a service that runs a shell snippet.
func shellSpec(name, script string) Spec {
	return Spec{Name: name, Command: "/bin/sh", Args: []string{"-c", script}, Env: os.Environ()}
}

// alive reports whether a pid still exists. Signal 0 performs the permission
// and existence checks without delivering anything.
func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

/* ------------------------------------------------------------- ordering -- */

// gatedHealth is a health endpoint that records when it was first probed and
// stays unready until it is opened.
type gatedHealth struct {
	server     *httptest.Server
	ready      atomic.Bool
	firstProbe atomic.Int64
}

func newGatedHealth(t *testing.T) *gatedHealth {
	t.Helper()
	gate := &gatedHealth{}
	gate.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gate.firstProbe.CompareAndSwap(0, time.Now().UnixNano())
		if !gate.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(gate.server.Close)
	return gate
}

func (g *gatedHealth) open()        { g.ready.Store(true) }
func (g *gatedHealth) probed() bool { return g.firstProbe.Load() != 0 }
func (g *gatedHealth) at() int64    { return g.firstProbe.Load() }

func TestStartAllWaitsForEachServiceBeforeStartingTheNext(t *testing.T) {
	gates := []*gatedHealth{newGatedHealth(t), newGatedHealth(t), newGatedHealth(t)}

	specs := make([]Spec, 0, len(gates))
	for i, gate := range gates {
		spec := shellSpec("svc"+strconv.Itoa(i), "sleep 30")
		spec.Health = &Health{URL: gate.server.URL, Timeout: 5 * time.Second}
		specs = append(specs, spec)
	}

	supervisor := NewSupervisor(quietPrinter(), time.Second)
	runner := &Runner{Supervisor: supervisor, Printer: quietPrinter()}
	t.Cleanup(supervisor.Shutdown)

	var startErr error
	finished := make(chan struct{})
	go func() {
		startErr = runner.StartAll(context.Background(), specs)
		close(finished)
	}()

	// Open the gates one at a time, checking that the next service has not
	// even been probed until the previous one is healthy.
	for i, gate := range gates {
		waitFor(t, fmt.Sprintf("svc%d to be probed", i), gate.probed)
		for _, later := range gates[i+1:] {
			if later.probed() {
				t.Fatalf("a later service was started before svc%d became healthy", i)
			}
		}
		gate.open()
	}

	<-finished
	if startErr != nil {
		t.Fatalf("StartAll: %v", startErr)
	}

	want := []string{"svc0", "svc1", "svc2"}
	if strings.Join(runner.Started, ",") != strings.Join(want, ",") {
		t.Fatalf("started order: want %v, got %v", want, runner.Started)
	}
	for i := 1; i < len(gates); i++ {
		if gates[i].at() <= gates[i-1].at() {
			t.Fatalf("svc%d was probed before svc%d", i, i-1)
		}
	}
}

func TestStartAllFailsWhenAServiceNeverBecomesHealthy(t *testing.T) {
	healthy := newGatedHealth(t)
	healthy.open()
	stuck := newGatedHealth(t) // never opened
	never := newGatedHealth(t)

	first := shellSpec("first", "sleep 30")
	first.Health = &Health{URL: healthy.server.URL, Timeout: 5 * time.Second}
	second := shellSpec("second", "echo booting; sleep 30")
	second.Health = &Health{URL: stuck.server.URL, Timeout: 400 * time.Millisecond}
	third := shellSpec("third", "sleep 30")
	third.Health = &Health{URL: never.server.URL, Timeout: 5 * time.Second}

	supervisor := NewSupervisor(quietPrinter(), time.Second)
	runner := &Runner{Supervisor: supervisor, Printer: quietPrinter()}
	t.Cleanup(supervisor.Shutdown)

	err := runner.StartAll(context.Background(), []Spec{first, second, third})
	if err == nil {
		t.Fatal("want an error when a service never becomes healthy")
	}

	var startErr *StartError
	if !errors.As(err, &startErr) {
		t.Fatalf("want a *StartError, got %T", err)
	}
	if startErr.Service != "second" {
		t.Fatalf("blamed the wrong service: %q", startErr.Service)
	}
	if !strings.Contains(err.Error(), stuck.server.URL) {
		t.Fatalf("the error should name the health URL it gave up on: %v", err)
	}
	// The tail of the service's own output is where the reason usually is.
	if !strings.Contains(err.Error(), "booting") {
		t.Fatalf("the error should carry the service's last output: %v", err)
	}
	if never.probed() {
		t.Fatal("a service after the failure must never be started")
	}
}

func TestStartAllReportsAServiceThatExitsDuringStartup(t *testing.T) {
	gate := newGatedHealth(t) // never opens; the process dies first

	spec := shellSpec("crasher", "echo 'bind: address already in use' >&2; exit 3")
	// A long health budget proves the failure is noticed by the exit, not by
	// the timeout.
	spec.Health = &Health{URL: gate.server.URL, Timeout: 30 * time.Second}

	supervisor := NewSupervisor(quietPrinter(), time.Second)
	runner := &Runner{Supervisor: supervisor, Printer: quietPrinter()}
	t.Cleanup(supervisor.Shutdown)

	started := time.Now()
	err := runner.StartAll(context.Background(), []Spec{spec})
	if err == nil {
		t.Fatal("want an error when a service exits during startup")
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("waited %s for a process that had already exited", elapsed)
	}
	if !strings.Contains(err.Error(), "exit code 3") {
		t.Fatalf("the error should report how it exited: %v", err)
	}
	if !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("the error should carry the service's own diagnostic: %v", err)
	}
}

/* ------------------------------------------------------------- shutdown -- */

func TestShutdownTerminatesEveryChild(t *testing.T) {
	supervisor := NewSupervisor(quietPrinter(), 2*time.Second)

	specs := []Spec{
		shellSpec("plain", "sleep 60"),
		// A service that ignores SIGTERM must still be gone afterwards.
		shellSpec("stubborn", "trap '' TERM; sleep 60"),
		// A service that exited on its own before shutdown: the supervisor
		// must neither hang waiting for it nor fail to stop the others.
		shellSpec("already-gone", "exit 0"),
		shellSpec("other", "sleep 60"),
	}

	pids := map[string]int{}
	for _, spec := range specs {
		service, err := supervisor.Launch(spec)
		if err != nil {
			t.Fatalf("launch %s: %v", spec.Name, err)
		}
		pids[spec.Name] = service.cmd.Process.Pid
	}

	waitFor(t, "already-gone to exit on its own", func() bool { return !alive(pids["already-gone"]) })

	started := time.Now()
	supervisor.Shutdown()
	elapsed := time.Since(started)

	for name, pid := range pids {
		if alive(pid) {
			t.Fatalf("%s (pid %d) survived shutdown", name, pid)
		}
	}
	// SIGTERM is honoured by three of the four, so this must not take the
	// full grace period multiplied by the number of services.
	if elapsed > 5*time.Second {
		t.Fatalf("shutdown took %s; services are being stopped serially", elapsed)
	}
	if names := supervisor.Running(); len(names) != 0 {
		t.Fatalf("still running after shutdown: %v", names)
	}
}

func TestShutdownIsSafeWhenEverythingAlreadyExited(t *testing.T) {
	supervisor := NewSupervisor(quietPrinter(), 2*time.Second)

	for _, script := range []string{"exit 0", "exit 7"} {
		service, err := supervisor.Launch(shellSpec("gone", script))
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		<-service.Done()
	}

	done := make(chan struct{})
	go func() {
		supervisor.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown hung on children that had already exited")
	}
}

// TestShutdownReapsGrandchildren is the reason children are placed in their
// own process group: a service that spawns a helper must not leave it holding
// a port after the stack stops.
func TestShutdownReapsGrandchildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "grandchild.pid")
	script := fmt.Sprintf("sleep 60 & echo $! > %s; trap '' TERM; sleep 60", marker)

	supervisor := NewSupervisor(quietPrinter(), 2*time.Second)
	if _, err := supervisor.Launch(shellSpec("parent", script)); err != nil {
		t.Fatalf("launch: %v", err)
	}

	var grandchild int
	waitFor(t, "the grandchild to record its pid", func() bool {
		raw, err := os.ReadFile(marker)
		if err != nil {
			return false
		}
		grandchild, err = strconv.Atoi(strings.TrimSpace(string(raw)))
		return err == nil && grandchild > 0 && alive(grandchild)
	})

	supervisor.Shutdown()

	// The kill reaches the group; the kernel may take a moment to tear it down.
	waitFor(t, "the grandchild to be gone", func() bool { return !alive(grandchild) })
}

func TestFailuresReportsAnUnexpectedExit(t *testing.T) {
	supervisor := NewSupervisor(quietPrinter(), time.Second)

	if _, err := supervisor.Launch(shellSpec("steady", "sleep 60")); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if _, err := supervisor.Launch(shellSpec("doomed", "echo dying >&2; exit 2")); err != nil {
		t.Fatalf("launch: %v", err)
	}

	select {
	case crashed := <-supervisor.Failures():
		if crashed.Spec.Name != "doomed" {
			t.Fatalf("reported the wrong service: %q", crashed.Spec.Name)
		}
		if got := crashed.ExitDescription(); got != "exit code 2" {
			t.Fatalf("exit description: %q", got)
		}
		if lines := crashed.LastLines(); len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"), "dying") {
			t.Fatalf("last output was not captured: %v", lines)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an unexpected exit was never reported")
	}

	supervisor.Shutdown()
}

// A service stopped on purpose is not a crash; reporting it would turn every
// clean Ctrl-C into a failure.
func TestShutdownDoesNotReportItsOwnStopsAsFailures(t *testing.T) {
	supervisor := NewSupervisor(quietPrinter(), 2*time.Second)
	for _, name := range []string{"a", "b"} {
		if _, err := supervisor.Launch(shellSpec(name, "sleep 60")); err != nil {
			t.Fatalf("launch %s: %v", name, err)
		}
	}

	supervisor.Shutdown()

	if crashed, ok := <-supervisor.Failures(); ok {
		t.Fatalf("%s was reported as a failure after a deliberate shutdown", crashed.Spec.Name)
	}
}

/* -------------------------------------------------------------- output -- */

func TestOutputIsPrefixedAndReassembledFromPartialWrites(t *testing.T) {
	var mu sync.Mutex
	var sink strings.Builder
	printer := NewPrinter(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return sink.WriteString(string(p))
	}), 4, false)

	supervisor := NewSupervisor(printer, time.Second)
	// printf without a trailing newline, twice, then one with: the two halves
	// must arrive as a single line.
	service, err := supervisor.Launch(shellSpec("svc", `printf 'hel'; sleep 0.1; printf 'lo\n'; echo second`))
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	<-service.Done()
	supervisor.Shutdown()

	mu.Lock()
	got := sink.String()
	mu.Unlock()

	if !strings.Contains(got, "svc  │ hello\n") {
		t.Fatalf("a line split across writes was not reassembled: %q", got)
	}
	if !strings.Contains(got, "svc  │ second\n") {
		t.Fatalf("missing the second line: %q", got)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

/* --------------------------------------------------------------- misc -- */

func TestPreflightNamesEveryBusyPort(t *testing.T) {
	busy := httptest.NewServer(http.NotFoundHandler())
	defer busy.Close()

	_, portText, _ := strings.Cut(strings.TrimPrefix(busy.URL, "http://"), ":")
	port, _ := strconv.Atoi(portText)

	err := Preflight("127.0.0.1", []Spec{
		{Name: "auth", Port: port},
		{Name: "data", Port: freePort(t)},
	})
	if err == nil {
		t.Fatal("want an error when a port is taken")
	}
	if !strings.Contains(err.Error(), "auth") || !strings.Contains(err.Error(), portText) {
		t.Fatalf("the error should name the service and port: %v", err)
	}

	if err := Preflight("127.0.0.1", []Spec{{Name: "data", Port: freePort(t)}}); err != nil {
		t.Fatalf("free ports must pass preflight: %v", err)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	probe := httptest.NewServer(http.NotFoundHandler())
	_, portText, _ := strings.Cut(strings.TrimPrefix(probe.URL, "http://"), ":")
	probe.Close()
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return port
}

func TestWaitForHealthStopsWhenTheContextIsCancelled(t *testing.T) {
	gate := newGatedHealth(t) // never opens

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	err := WaitForHealth(ctx, HTTPProbe(gate.server.URL, nil), time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %s to be noticed", elapsed)
	}
}

/* -------------------------------------------------------------- signals -- */

// One Ctrl-C must drain the stack, not kill it. The regression this pins:
// two independent signal registrations both receive the same press, so the
// force-kill path fired on the first one and services were SIGKILLed before
// they could close their listeners.
func TestSignalPhasesNeedsTwoPressesToForce(t *testing.T) {
	signals := make(chan os.Signal, 4)
	interrupted, forced := SignalPhases(signals)

	signals <- syscall.SIGINT
	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Fatal("the first signal must ask for a graceful stop")
	}
	select {
	case <-forced:
		t.Fatal("one signal escalated straight to a force-kill")
	case <-time.After(100 * time.Millisecond):
	}

	signals <- syscall.SIGTERM
	select {
	case <-forced:
	case <-time.After(time.Second):
		t.Fatal("the second signal must force")
	}
}
