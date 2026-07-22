package engine

import "time"

// Clock abstracts wall-clock time so the lease heartbeat's suspend-gap
// detector and the stage timeout watchdog can be tested without real
// sleeps, and so production always compares calendar time — never a
// monotonic reading, which pauses for a suspended machine's entire
// downtime — when deciding whether a lease or a stage has run out of
// budget.
type Clock interface {
	Now() time.Time
}

// realClock strips the monotonic reading time.Now() attaches (Round(0)) so
// Sub between two realClock readings is always wall-clock elapsed time,
// suspend included: CLOCK_REALTIME keeps advancing across a suspend even
// though CLOCK_MONOTONIC — what context.WithTimeout and time.Ticker are
// driven by — does not.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now().Round(0) }
