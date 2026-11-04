package appfactory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Event is one progress update shown while a prompt runs.
type Event struct {
	Seq  int       `json:"seq"`
	Kind string    `json:"kind"` // status | tool | message | usage | build | error | done
	Text string    `json:"text"`
	OK   *bool     `json:"ok,omitempty"`
	Time time.Time `json:"time"`
}

// Job is one prompt run. Its events are kept in memory so a page reload can
// replay them.
type Job struct {
	ID      string    `json:"id"`
	Slug    string    `json:"slug"`
	Prompt  string    `json:"prompt"`
	Started time.Time `json:"started"`

	mu      sync.Mutex
	events  []Event
	done    bool
	ok      bool
	changed chan struct{}
}

func (j *Job) emit(kind, text string, ok *bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.append(kind, text, ok)
}

// append adds an event and wakes waiters. Callers hold j.mu.
func (j *Job) append(kind, text string, ok *bool) {
	j.events = append(j.events, Event{Seq: len(j.events), Kind: kind, Text: text, OK: ok, Time: time.Now()})
	close(j.changed)
	j.changed = make(chan struct{})
}

// finish records the final event and the outcome together, so a waiter woken
// by the "done" event always sees the job as finished.
func (j *Job) finish(ok bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.done, j.ok = true, ok
	j.append("done", map[bool]string{true: "Done", false: "Failed"}[ok], &ok)
}

// Done reports whether the job finished and whether it succeeded.
func (j *Job) Done() (done, ok bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done, j.ok
}

// Next blocks until there are events after index `after`, the job finishes, or
// ctx ends. It returns the new events and whether the job is finished.
func (j *Job) Next(ctx context.Context, after int) ([]Event, bool) {
	for {
		j.mu.Lock()
		if after < len(j.events) || j.done {
			events := append([]Event(nil), j.events[min(after, len(j.events)):]...)
			done := j.done
			j.mu.Unlock()
			return events, done
		}
		wait := j.changed
		j.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, false
		}
	}
}

// Jobs indexes running and recent jobs.
type Jobs struct {
	mu     sync.Mutex
	byID   map[string]*Job
	latest map[string]*Job
}

// NewJobs returns an empty registry.
func NewJobs() *Jobs {
	return &Jobs{byID: map[string]*Job{}, latest: map[string]*Job{}}
}

const jobRetention = time.Hour

func (r *Jobs) start(slug, prompt string) *Job {
	var b [12]byte
	_, _ = rand.Read(b[:])
	j := &Job{ID: hex.EncodeToString(b[:]), Slug: slug, Prompt: prompt, Started: time.Now(), changed: make(chan struct{})}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, old := range r.byID {
		if done, _ := old.Done(); done && time.Since(old.Started) > jobRetention {
			delete(r.byID, id)
			if r.latest[old.Slug] == old {
				delete(r.latest, old.Slug)
			}
		}
	}
	r.byID[j.ID] = j
	r.latest[slug] = j
	return j
}

// Get returns a job by ID if it belongs to slug.
func (r *Jobs) Get(slug, id string) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	if j := r.byID[id]; j != nil && j.Slug == slug {
		return j
	}
	return nil
}

// Latest returns the most recent job for an app, if still retained.
func (r *Jobs) Latest(slug string) *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	j := r.latest[slug]
	if j != nil && r.byID[j.ID] == nil {
		return nil
	}
	return j
}
