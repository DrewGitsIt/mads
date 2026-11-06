package runner

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/linkwrap"
	"github.com/drewkidwell/mads/pkg/turndigest"
	"github.com/drewkidwell/mads/providers/provider"
)

// Held link updates riding with a user send (docs/HELD-UPDATES-DESIGN.md):
// the wire, what the digest sees, the audit, and the live frames.

var attachedParts = []contracts.InjectPart{
	{Text: "did a", Attribution: contracts.Attribution{LinkID: "lnk_a", SourceName: "Alpha", Kind: "digest", Relation: "a session you lead", Turn: 3, DigestAttributed: true}},
	{Text: "did b", Attribution: contracts.Attribution{LinkID: "lnk_b", SourceName: "Beta", Kind: "signal", DigestAttributed: true}},
}

const attachedWire = "<mads-message kind=\"digest\" from=\"Alpha\" relation=\"a session you lead\" turn=\"3\" attached=\"true\">\ndid a\n</mads-message>\n\n" +
	"<mads-message kind=\"signal\" from=\"Beta\" attached=\"true\">\ndid b\n</mads-message>\n\n" +
	"what now?"

func userDigestTexts(r *Runner) []string {
	r.digest.mu.Lock()
	defer r.digest.mu.Unlock()
	var out []string
	for _, e := range r.digest.buf {
		if e.Kind == turndigest.KindUser {
			out = append(out, e.Text)
		}
	}
	return out
}

func TestSendWithAttachedUpdates(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)
	sub := rig.r.SubscribeChat("")
	defer sub.Close()

	if err := rig.r.Send(ctx, "what now?", attachedParts...); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the send", func() bool { return len(rig.sess.sentTexts()) == 1 })
	if got := rig.sess.sentTexts()[0]; got != attachedWire {
		t.Fatalf("wire =\n%s\nwant\n%s", got, attachedWire)
	}
	if got := userDigestTexts(rig.r); len(got) != 1 || got[0] != "what now?" {
		t.Errorf("digest saw %q, want only the user's text", got)
	}

	// One audit per attached update, under the link's own actor.
	var audits []map[string]string
	waitFor(t, "the audits", func() bool {
		audits = eventsOfType[map[string]string](t, rig.data, rig.id, contracts.LinkMessageInjected)
		return len(audits) == 2
	})
	if audits[0]["text"] != "did a" || audits[1]["link_id"] != "lnk_b" {
		t.Errorf("audits = %+v", audits)
	}

	// The live user_message frames segment as the durable transcript will.
	var frames []userMessageData
	for _, f := range drain(sub.Live) {
		if f.Kind == frameUserMessage {
			var d userMessageData
			_ = json.Unmarshal(f.Data, &d)
			frames = append(frames, d)
		}
	}
	if len(frames) != 3 {
		t.Fatalf("user_message frames = %+v, want 2 updates + the human row", frames)
	}
	base := strings.TrimSuffix(frames[0].Key, "#0")
	durable := linkwrap.Expand([]contracts.TranscriptMessage{{Role: "user", Text: attachedWire, Key: base}})
	for i, f := range frames {
		if f.Key != durable[i].Key || f.Text != durable[i].Text {
			t.Errorf("frame %d = (%q, %q), durable row (%q, %q)", i, f.Key, f.Text, durable[i].Key, durable[i].Text)
		}
	}
	if a := frames[0].Attribution; a == nil || !a.Attached || a.SourceName != "Alpha" {
		t.Errorf("frame 0 attribution = %+v", a)
	}
	if frames[2].Attribution != nil || frames[2].Text != "what now?" {
		t.Errorf("human frame = %+v", frames[2])
	}
}

// A queued user message keeps its attached updates and delivers them with
// its text once the turn ahead of it ends.
func TestQueuedSendKeepsAttachedUpdates(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)
	if err := rig.r.Send(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	q, err := rig.r.SendOrQueue(ctx, "what now?", attachedParts...)
	if err != nil || q == nil {
		t.Fatalf("SendOrQueue = %+v, %v; want queued", q, err)
	}
	if q.Text != "what now?" {
		t.Errorf("queued text = %q, want the user's own", q.Text)
	}
	rig.sess.events <- provider.Event{Kind: provider.EventTurnEnd}
	waitFor(t, "the queued send", func() bool { return len(rig.sess.sentTexts()) == 2 })
	if got := rig.sess.sentTexts()[1]; got != attachedWire {
		t.Fatalf("queued wire =\n%s\nwant\n%s", got, attachedWire)
	}
	if got := userDigestTexts(rig.r); len(got) != 1 || got[0] != "what now?" {
		t.Errorf("digest saw %q, want only the user's text", got)
	}
}

// Cancelling a queued message doesn't drop the updates it carried: they
// stay queued as an injection of their own.
func TestCancelQueuedKeepsAttachedUpdates(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)
	if err := rig.r.Send(ctx, "first"); err != nil {
		t.Fatal(err)
	}
	q, err := rig.r.SendOrQueue(ctx, "never mind", attachedParts...)
	if err != nil || q == nil {
		t.Fatalf("SendOrQueue = %+v, %v", q, err)
	}
	if !rig.r.CancelQueued(q.ID) {
		t.Fatal("CancelQueued = false")
	}
	if got := rig.r.QueuedMessages(); len(got) != 1 || got[0].Attribution == nil || got[0].ID == q.ID {
		t.Fatalf("queue after cancel = %+v, want the updates as an injection", got)
	}
	rig.sess.events <- provider.Event{Kind: provider.EventTurnEnd}
	waitFor(t, "the updates", func() bool { return len(rig.sess.sentTexts()) == 2 })
	want := linkwrap.Wrap("did a", attachedParts[0].Attribution) + "\n\n" + linkwrap.Wrap("did b", attachedParts[1].Attribution)
	if got := rig.sess.sentTexts()[1]; got != want {
		t.Fatalf("delivered =\n%s\nwant\n%s", got, want)
	}
}

// An owed re-root note stays outermost, ahead of the attached updates.
func TestRerootNoteAheadOfAttached(t *testing.T) {
	ctx := context.Background()
	rig := startPeer(t)
	rig.r.mu.Lock()
	rig.r.rerootNote = "[mads] note"
	rig.r.mu.Unlock()
	if err := rig.r.Send(ctx, "what now?", attachedParts...); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the send", func() bool { return len(rig.sess.sentTexts()) == 1 })
	if got := rig.sess.sentTexts()[0]; got != "[mads] note\n\n"+attachedWire {
		t.Fatalf("wire = %q", got)
	}
	segs, ok := linkwrap.Parse(rig.sess.sentTexts()[0])
	if !ok || len(segs) != 4 || segs[0].Block != nil || segs[0].Human || !segs[3].Human {
		t.Errorf("Parse = %+v", segs)
	}
}
