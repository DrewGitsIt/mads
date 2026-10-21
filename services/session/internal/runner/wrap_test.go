package runner

import (
	"context"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/linkwrap"
	"github.com/drewkidwell/mads/providers/provider"
)

// What the provider is handed: the human's own words as typed, every
// injection inside its wrapper — sent at once or drained from the queue, and
// each digest of a coalesced run wrapped on its own. An empty Kind is a
// "message".
func TestInjectionsReachTheProviderWrapped(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)
	endTurn := func(n int) {
		t.Helper()
		rig.sess.events <- provider.Event{Kind: provider.EventTurnEnd}
		waitFor(t, "the next send", func() bool { return len(rig.sess.sentTexts()) == n })
	}

	msg := contracts.Attribution{LinkID: "lnk_l", SourceName: "Lead"}
	if err := rig.r.Inject(ctx, "hello", msg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the inject", func() bool { return len(rig.sess.sentTexts()) == 1 })

	digest := contracts.Attribution{LinkID: "lnk_m", SourceName: "Worker", DigestAttributed: true, Kind: "digest", Relation: "a session you lead", Turn: 7}
	if err := rig.r.Inject(ctx, "d1", digest); err != nil {
		t.Fatal(err)
	}
	digest.Turn = 8
	if err := rig.r.Inject(ctx, "d2", digest); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.r.SendOrQueue(ctx, "mine"); err != nil {
		t.Fatal(err)
	}
	endTurn(2)
	endTurn(3)

	sent := rig.sess.sentTexts()
	want := []string{
		"<mads-message kind=\"message\" from=\"Lead\">\nhello\n</mads-message>",
		"<mads-message kind=\"digest\" from=\"Worker\" relation=\"a session you lead\" turn=\"7\">\nd1\n</mads-message>\n\n" +
			"<mads-message kind=\"digest\" from=\"Worker\" relation=\"a session you lead\" turn=\"8\">\nd2\n</mads-message>",
		"mine",
	}
	for i := range want {
		if sent[i] != want[i] {
			t.Errorf("send %d = %q\nwant %q", i, sent[i], want[i])
		}
	}
	segs, ok := linkwrap.Parse(sent[1])
	if !ok || len(segs) != 2 || segs[0].Block.Body != "d1" || segs[1].Block.Attribution.Turn != 8 {
		t.Errorf("coalesced run parses as %+v, want two digests", segs)
	}

	rig.sess.events <- provider.Event{Kind: provider.EventTurnEnd}
	waitFor(t, "the turn to end", func() bool { return !rig.r.isBusy() })
	if err := rig.r.Send(ctx, "plain"); err != nil {
		t.Fatal(err)
	}
	if got := rig.sess.sentTexts()[3]; got != "plain" {
		t.Errorf("user send = %q, want it unwrapped", got)
	}
}
