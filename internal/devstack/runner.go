package devstack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Bringing the stack up in order.
//
// Each service is launched, then proven healthy, before the next is launched.
// Starting everything at once would be faster and would also reduce a real
// dependency failure — the control plane refusing to start because its
// database is locked, say — into a scattering of connection errors from
// whichever service happened to call it first.

// Runner starts a plan under a Supervisor.
type Runner struct {
	Supervisor *Supervisor
	Printer    *Printer

	// Started lists the services that reached healthy, in order.
	Started []string
}

// StartError describes a service that never came up, carrying the tail of its
// own output: the reason is almost always in the last few lines it printed,
// and making the developer scroll for it is a poor trade.
type StartError struct {
	Service string
	Reason  error
	Output  []string
}

func (e *StartError) Error() string {
	message := fmt.Sprintf("%s never became healthy: %v", e.Service, e.Reason)
	if len(e.Output) > 0 {
		message += "\n  last output from " + e.Service + ":\n    " + strings.Join(e.Output, "\n    ")
	}
	return message
}

func (e *StartError) Unwrap() error { return e.Reason }

// errExited reports a service that quit instead of becoming ready.
var errExited = errors.New("it exited during startup")

// StartAll launches every spec in order, waiting for each to report healthy.
func (r *Runner) StartAll(ctx context.Context, specs []Spec) error {
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return err
		}

		if spec.Port > 0 {
			r.say(fmt.Sprintf("starting %s on port %d…", spec.Name, spec.Port))
		} else {
			r.say("starting " + spec.Name + "…")
		}

		service, err := r.Supervisor.Launch(spec)
		if err != nil {
			return &StartError{Service: spec.Name, Reason: err}
		}

		if spec.Health == nil {
			r.Started = append(r.Started, spec.Name)
			continue
		}

		if err := r.waitHealthy(ctx, service, spec); err != nil {
			return err
		}
		r.Started = append(r.Started, spec.Name)
		r.say(spec.Name + " healthy.")
	}
	return nil
}

// waitHealthy races readiness against the process dying, so a service that
// crashes on startup is reported in milliseconds rather than after the full
// health timeout.
func (r *Runner) waitHealthy(ctx context.Context, service *Service, spec Spec) error {
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go func() {
		select {
		case <-service.Done():
			cancel()
		case <-probeCtx.Done():
		}
	}()

	err := WaitForHealth(probeCtx, HTTPProbe(spec.Health.URL, spec.Health.Accept), spec.Health.Timeout)
	if err == nil {
		return nil
	}

	if service.Exited() {
		return &StartError{
			Service: spec.Name,
			Reason:  fmt.Errorf("%w (%s)", errExited, service.ExitDescription()),
			Output:  service.LastLines(),
		}
	}
	// A cancelled parent context is a shutdown, not a startup failure.
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return &StartError{
		Service: spec.Name,
		Reason:  fmt.Errorf("%w at %s", err, spec.Health.URL),
		Output:  service.LastLines(),
	}
}

func (r *Runner) say(message string) {
	if r.Printer != nil {
		r.Printer.Line("stack", "gray", message)
	}
}

// Preflight refuses to start when something already holds a port.
//
// Binding would fail anyway, but one message naming every conflict is far
// more useful than whichever service happens to crash first.
func Preflight(host string, specs []Spec) error {
	var busy []string
	for _, spec := range specs {
		if spec.Port > 0 && !PortFree(host, spec.Port) {
			busy = append(busy, fmt.Sprintf("%d (%s)", spec.Port, spec.Name))
		}
	}
	if len(busy) == 0 {
		return nil
	}

	plural := ""
	if len(busy) > 1 {
		plural = "s"
	}
	return fmt.Errorf("port%s already in use: %s — stop whatever holds %s, or pass different -*-port flags",
		plural, strings.Join(busy, ", "), map[bool]string{true: "them", false: "it"}[len(busy) > 1])
}

// DefaultGrace is how long a service gets to exit on SIGTERM. It is longer
// than a clean HTTP drain and shorter than a developer's patience.
const DefaultGrace = 8 * time.Second
