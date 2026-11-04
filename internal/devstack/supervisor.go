package devstack

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"
)

// One supervised child process.
//
// Two decisions here are what make shutdown reliable, and both are invisible
// in the happy path:
//
//  1. Children are placed in their own process group (see process_unix.go) and
//     signalled by group, so anything a service spawns dies with it. A service
//     launched through a wrapper would otherwise leak the wrapped process and
//     keep its port bound — the classic "address already in use" on restart.
//  2. A terminal's Ctrl-C reaches only this supervisor, not the children,
//     because they are no longer in the foreground group. Shutdown is
//     therefore entirely ours to sequence rather than a race between the
//     terminal's signal and our own.

// Health describes how to tell a service is ready.
type Health struct {
	URL string
	// Accept decides which status codes count as ready. Nil means 200 only.
	Accept func(status int) bool
	// Timeout bounds the wait before the stack gives up on this service.
	Timeout time.Duration
}

// Spec is one service to run.
type Spec struct {
	Name string
	// Command is an absolute path to an executable. Services are compiled
	// before launch rather than run through `go run`, which would put a
	// wrapper process between the supervisor and the listener.
	Command string
	Args    []string
	Dir     string
	// Env is the complete environment, already resolved. Nothing is
	// inherited implicitly, so a stale value in the developer's shell cannot
	// desync two services that must agree on a shared secret.
	Env   []string
	Color string
	Port  int
	// Health is nil for a service with nothing to probe, such as the asset
	// watcher, which is ready as soon as it is running.
	Health *Health
}

// Service is a running Spec.
type Service struct {
	Spec Spec

	cmd  *exec.Cmd
	done chan struct{}

	mu       sync.Mutex
	exitErr  error
	exited   bool
	stopping bool
	recent   []string
}

const keepLines = 20

// Start launches the process and begins streaming its output.
func Start(spec Spec, printer *Printer) (*Service, error) {
	cmd := exec.Command(spec.Command, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	cmd.Stdin = nil
	setProcessGroup(cmd)

	service := &Service{Spec: spec, cmd: cmd, done: make(chan struct{})}

	writer := newLineWriter(func(line string) {
		service.record(line)
		printer.Line(spec.Name, spec.Color, line)
	})
	// Both streams share one line assembler so a service's stderr and stdout
	// stay in the order it produced them.
	cmd.Stdout = writer
	cmd.Stderr = writer

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.Name, err)
	}

	go func() {
		err := cmd.Wait()
		writer.Flush()

		service.mu.Lock()
		service.exited, service.exitErr = true, err
		service.mu.Unlock()
		close(service.done)
	}()

	return service, nil
}

func (s *Service) record(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recent = append(s.recent, line)
	if len(s.recent) > keepLines {
		s.recent = s.recent[len(s.recent)-keepLines:]
	}
}

// LastLines returns the tail of this service's output, for a failure report.
func (s *Service) LastLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.recent...)
}

// Done is closed once the process has exited and been reaped.
func (s *Service) Done() <-chan struct{} { return s.done }

// Exited reports whether the process is already gone.
func (s *Service) Exited() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exited
}

// markStopping records that this exit was asked for, so it is not reported as
// a crash.
func (s *Service) markStopping() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = true
}

func (s *Service) wasStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

// ExitDescription explains how the process ended, in the terms a developer
// reading one line of terminal output needs.
func (s *Service) ExitDescription() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.exited {
		return "still running"
	}
	if s.exitErr == nil {
		return "exit code 0"
	}
	var exitErr *exec.ExitError
	if errors.As(s.exitErr, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return "killed by " + status.Signal().String()
		}
		return fmt.Sprintf("exit code %d", exitErr.ExitCode())
	}
	return s.exitErr.Error()
}

// signal delivers sig to the whole process group, tolerating a process that
// has already gone. A child racing us to exit is the normal case during
// shutdown, not an error.
func (s *Service) signal(sig syscall.Signal) {
	if s.Exited() {
		return
	}
	if err := signalGroup(s.cmd, sig); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return
		}
	}
}

// Stop asks the process to exit, escalating to an unignorable kill if it has
// not gone within grace. It returns only once the process has been reaped, so
// a caller that returns after Stop knows the port is free.
func (s *Service) Stop(grace time.Duration) {
	s.markStopping()

	select {
	case <-s.done:
		return
	default:
	}

	s.signal(syscall.SIGTERM)

	select {
	case <-s.done:
		return
	case <-time.After(grace):
	}

	s.signal(syscall.SIGKILL)
	<-s.done
}

/* ----------------------------------------------------------- supervisor -- */

// Supervisor owns the running services and their shutdown.
type Supervisor struct {
	printer *Printer
	grace   time.Duration

	mu       sync.Mutex
	services []*Service
	stopping bool

	failures chan *Service
	watching sync.WaitGroup
}

// NewSupervisor builds an empty Supervisor. grace is how long a child gets to
// exit on SIGTERM before it is killed.
func NewSupervisor(printer *Printer, grace time.Duration) *Supervisor {
	return &Supervisor{printer: printer, grace: grace, failures: make(chan *Service, 8)}
}

// Failures reports services that exited without being asked to.
func (sv *Supervisor) Failures() <-chan *Service { return sv.failures }

// Launch starts one service and watches it for an unexpected exit.
func (sv *Supervisor) Launch(spec Spec) (*Service, error) {
	service, err := Start(spec, sv.printer)
	if err != nil {
		return nil, err
	}

	sv.mu.Lock()
	sv.services = append(sv.services, service)
	sv.mu.Unlock()

	sv.watching.Add(1)
	go func() {
		defer sv.watching.Done()
		<-service.Done()
		if service.wasStopping() || sv.isStopping() {
			return
		}
		select {
		case sv.failures <- service:
		default:
		}
	}()

	return service, nil
}

func (sv *Supervisor) isStopping() bool {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	return sv.stopping
}

// Running lists the services that have not exited, for a status line.
func (sv *Supervisor) Running() []string {
	sv.mu.Lock()
	defer sv.mu.Unlock()

	var names []string
	for _, service := range sv.services {
		if !service.Exited() {
			names = append(names, service.Spec.Name)
		}
	}
	sort.Strings(names)
	return names
}

// Shutdown stops every service and returns once they are all reaped.
//
// Order is the reverse of startup, so a dependency outlives its dependants and
// nothing spends its last moments logging connection failures. A service that
// already exited on its own is skipped rather than signalled, which is the
// case the Node supervisor got wrong: it would wait out the full grace period
// for a process that was never there.
func (sv *Supervisor) Shutdown() {
	sv.mu.Lock()
	if sv.stopping {
		sv.mu.Unlock()
		return
	}
	sv.stopping = true
	services := append([]*Service(nil), sv.services...)
	sv.mu.Unlock()

	// Signal everything first, then wait. Stopping them one at a time would
	// serialise the grace period per service.
	for i := len(services) - 1; i >= 0; i-- {
		services[i].markStopping()
		services[i].signal(syscall.SIGTERM)
	}

	var wg sync.WaitGroup
	for _, service := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-service.Done():
			case <-time.After(sv.grace):
				sv.printer.Line("stack", "gray", service.Spec.Name+" did not stop in time — killing it")
				service.signal(syscall.SIGKILL)
				<-service.Done()
			}
		}()
	}
	wg.Wait()

	sv.watching.Wait()
	close(sv.failures)
}

// Kill terminates everything immediately, for a second Ctrl-C.
func (sv *Supervisor) Kill() {
	sv.mu.Lock()
	sv.stopping = true
	services := append([]*Service(nil), sv.services...)
	sv.mu.Unlock()

	for _, service := range services {
		service.markStopping()
		service.signal(syscall.SIGKILL)
	}
	for _, service := range services {
		<-service.Done()
	}
}
