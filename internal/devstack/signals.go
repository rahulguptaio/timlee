package devstack

import "os"

// SignalPhases splits a stream of termination signals into two phases: the
// first press asks for a graceful stop, the second insists.
//
// This exists as one function because the obvious implementation is subtly
// wrong. Registering two independent signal notifications — one to cancel a
// context, one to watch for a second press — delivers the *same* press to
// both, so a single Ctrl-C is immediately read as "asked twice" and the stack
// is killed instead of drained. Reading both phases from one channel makes
// that impossible.
func SignalPhases(signals <-chan os.Signal) (interrupted, forced <-chan struct{}) {
	first := make(chan struct{})
	second := make(chan struct{})

	go func() {
		if _, ok := <-signals; !ok {
			return
		}
		close(first)
		if _, ok := <-signals; !ok {
			return
		}
		close(second)
	}()

	return first, second
}
