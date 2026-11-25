package meshnet

import (
	"context"
	"errors"
	"time"
)

// Race is F3 step 3: happy eyeballs (RFC 8305) across a peer's
// candidates. The first attempt starts at once; each next one starts
// Stagger later without cancelling those already going, or at once when
// one fails. The first to finish wins and the rest are cancelled. What
// "finish" means is dial's to say: mads's dial completes the TLS handshake
// with the pinned key, because a plain TCP connect isn't success (on
// another network the same private address may belong to someone else's
// machine).

// RaceOptions tunes Race. Zero values get F3's defaults.
type RaceOptions struct {
	// Stagger is the head start each attempt gets (250 ms).
	Stagger time.Duration
	// Attempt bounds one attempt, TCP plus TLS (3 s).
	Attempt time.Duration
	// Total bounds the whole race (6 s).
	Total time.Duration
	Clock Clock
}

const (
	DefaultStagger = 250 * time.Millisecond
	DefaultAttempt = 3 * time.Second
	DefaultTotal   = 6 * time.Second
)

func (o RaceOptions) withDefaults() RaceOptions {
	if o.Stagger <= 0 {
		o.Stagger = DefaultStagger
	}
	if o.Attempt <= 0 {
		o.Attempt = DefaultAttempt
	}
	if o.Total <= 0 {
		o.Total = DefaultTotal
	}
	if o.Clock == nil {
		o.Clock = RealClock{}
	}
	return o
}

// Attempt is one candidate's part in a race.
type Attempt struct {
	Candidate Candidate
	// Started: it was dialled at all (a race won early never starts the
	// rest).
	Started bool
	Err     error
	Took    time.Duration
	Won     bool
}

// ErrNoneAnswered is a race where no candidate finished.
var ErrNoneAnswered = errors.New("no address answered")

// ErrRaceTimeout is a race that ran out of time.
var ErrRaceTimeout = errors.New("no address answered in time")

// ErrAttemptTimeout is one attempt that ran out of time.
var ErrAttemptTimeout = errors.New("timed out")

// RaceError is a race nothing won, with every attempt.
type RaceError struct {
	Err      error
	Attempts []Attempt
}

func (e *RaceError) Error() string {
	for _, a := range e.Attempts {
		if a.Err != nil {
			return e.Err.Error() + " (last: " + a.Candidate.Addr + ": " + a.Err.Error() + ")"
		}
	}
	return e.Err.Error()
}

func (e *RaceError) Unwrap() error { return e.Err }

type raceResult[T any] struct {
	i   int
	v   T
	err error
	at  time.Time
}

// Race dials cands (already ranked and filtered) and returns the winner's
// value and index. release is called for any value that finishes after
// the race is decided (nil: dropped). With no winner the error is a
// *RaceError carrying every attempt.
func Race[T any](ctx context.Context, cands []Candidate, o RaceOptions, dial func(context.Context, Candidate) (T, error), release func(T)) (T, int, []Attempt, error) {
	o = o.withDefaults()
	var zero T
	attempts := make([]Attempt, len(cands))
	for i, c := range cands {
		attempts[i].Candidate = c
	}
	if len(cands) == 0 {
		return zero, -1, attempts, &RaceError{Err: ErrNoneAnswered, Attempts: attempts}
	}
	raceCtx, cancelAll := context.WithCancel(ctx)
	results := make(chan raceResult[T], len(cands))
	start := func(i int) {
		attempts[i].Started = true
		actx, cancel := context.WithCancel(raceCtx)
		begun := o.Clock.Now()
		timeout := o.Clock.After(o.Attempt)
		done := make(chan struct{})
		go func() {
			select {
			case <-timeout:
				cancel()
			case <-done:
			}
		}()
		go func() {
			v, err := dial(actx, cands[i])
			timedOut := false
			select {
			case <-actx.Done():
				timedOut = err != nil && raceCtx.Err() == nil
			default:
			}
			close(done)
			cancel()
			if timedOut {
				err = ErrAttemptTimeout
			}
			results <- raceResult[T]{i: i, v: v, err: err, at: begun}
		}()
	}
	next, running := 0, 0
	launch := func() {
		start(next)
		next++
		running++
	}
	launch()
	total := o.Clock.After(o.Total)
	var stagger <-chan time.Time
	if next < len(cands) {
		stagger = o.Clock.After(o.Stagger)
	}
	finish := func() {
		cancelAll()
		// Late finishers are released in the background: the race is
		// decided, nobody waits for them.
		left := running
		go func() {
			for ; left > 0; left-- {
				r := <-results
				if r.err == nil && release != nil {
					release(r.v)
				}
			}
		}()
	}
	for {
		select {
		case r := <-results:
			running--
			attempts[r.i].Took = o.Clock.Now().Sub(r.at)
			if r.err == nil {
				attempts[r.i].Won = true
				finish()
				return r.v, r.i, attempts, nil
			}
			attempts[r.i].Err = r.err
			if next < len(cands) {
				// A failure starts the next one at once.
				launch()
				if next < len(cands) {
					stagger = o.Clock.After(o.Stagger)
				} else {
					stagger = nil
				}
			} else if running == 0 {
				cancelAll()
				return zero, -1, attempts, &RaceError{Err: ErrNoneAnswered, Attempts: attempts}
			}
		case <-stagger:
			launch()
			if next < len(cands) {
				stagger = o.Clock.After(o.Stagger)
			} else {
				stagger = nil
			}
		case <-total:
			for i := range attempts {
				if attempts[i].Started && attempts[i].Err == nil {
					attempts[i].Err = ErrAttemptTimeout
				}
			}
			finish()
			return zero, -1, attempts, &RaceError{Err: ErrRaceTimeout, Attempts: attempts}
		case <-ctx.Done():
			finish()
			return zero, -1, attempts, &RaceError{Err: ctx.Err(), Attempts: attempts}
		}
	}
}
