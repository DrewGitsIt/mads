package conformance

import "testing"

// These are white-box tests of refAnnounceAfterTurnDone: the deterministic
// tie-break checker.refAnnounce() falls back to when select lands on
// turnDone. select gives no priority to whichever channel became ready
// first in real time, so a compliant provider (which always announces
// before it can emit turn-end — see fakeSession.Send,
// providers/fake/fake.go) can still have select pick turnDone under
// scheduler pressure. refAnnounceAfterTurnDone must not treat that alone as
// a failure; these construct each reachable state directly (no goroutines,
// no timing) rather than trying to force select's tie-break in a real race.

// TestRefAnnounceAfterTurnDoneRace simulates select landing on turnDone
// even though the provider had already announced: the buffered channel
// already holds the value before refAnnounceAfterTurnDone ever runs, same
// as a compliant provider under scheduler pressure.
func TestRefAnnounceAfterTurnDoneRace(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "ref-1"
	if err := refAnnounceAfterTurnDone(true, ch, func() string { return "" }); err != nil {
		t.Errorf("announced-but-select-picked-turnDone must not fail: %v", err)
	}
}

// TestRefAnnounceAfterTurnDoneViaRef covers providers that surface the ref
// via Ref() rather than a pending RefAnnounced value once announced.
func TestRefAnnounceAfterTurnDoneViaRef(t *testing.T) {
	ch := make(chan string)
	if err := refAnnounceAfterTurnDone(true, ch, func() string { return "ref-1" }); err != nil {
		t.Errorf("ref visible via Ref() must not fail: %v", err)
	}
}

// TestRefAnnounceAfterTurnDoneGenuinelyLate proves a truly non-compliant
// provider (never announced by turn-end, via either channel) is still
// caught — this fix removes false positives, not the check itself.
func TestRefAnnounceAfterTurnDoneGenuinelyLate(t *testing.T) {
	ch := make(chan string, 1)
	if err := refAnnounceAfterTurnDone(true, ch, func() string { return "" }); err == nil {
		t.Error("want error: turn completed with no announcement at all")
	}
}

// TestRefAnnounceAfterTurnDoneUnsupportedHonest proves a declared-unsupported
// provider that never announces is not flagged.
func TestRefAnnounceAfterTurnDoneUnsupportedHonest(t *testing.T) {
	ch := make(chan string, 1)
	if err := refAnnounceAfterTurnDone(false, ch, func() string { return "" }); err != nil {
		t.Errorf("declared-unsupported, never announced: want nil, got %v", err)
	}
}
