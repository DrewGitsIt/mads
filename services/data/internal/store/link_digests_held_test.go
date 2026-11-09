package store

import (
	"context"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// held appends n deferred pushes ("h0".."h<n-1>") on link and returns their
// ids in order.
func held(t *testing.T, links *Links, link contracts.Link, n int, attr *contracts.Attribution) []int64 {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := links.AppendDigest(ctx, link.ID, &contracts.LinkDigest{
			Text: "h" + string(rune('0'+i)), Deferred: true, Noop: i%2 == 0, Attribution: attr,
		}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := links.HeldUpdates(ctx, link.GrantingSessionID)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, h := range list.Held {
		ids = append(ids, h.ID)
	}
	return ids
}

func TestHeldUpdatesListShape(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	a := mkSession(t, sessions, contracts.Session{Name: "worker a"})
	b := mkSession(t, sessions, contracts.Session{Name: "worker b"})
	la := contracts.Link{GrantingSessionID: leader.ID, SubjectSessionID: a.ID, Grants: []contracts.Grant{contracts.GrantRead}}
	lb := contracts.Link{GrantingSessionID: leader.ID, SubjectSessionID: b.ID, Grants: []contracts.Grant{contracts.GrantRead}}
	for _, l := range []*contracts.Link{&la, &lb} {
		if err := links.Create(ctx, l, "user"); err != nil {
			t.Fatal(err)
		}
	}
	// One with a stored attribution, one without (name falls back to the
	// subject session's), one on_signal backlog entry (never listed).
	if err := links.AppendDigest(ctx, la.ID, &contracts.LinkDigest{Text: "from a", Deferred: true, Noop: true,
		Attribution: &contracts.Attribution{LinkID: la.ID, SourceName: "A (snapshot)", Kind: "digest"}}); err != nil {
		t.Fatal(err)
	}
	if err := links.AppendDigest(ctx, lb.ID, &contracts.LinkDigest{Text: "from b", Deferred: true}); err != nil {
		t.Fatal(err)
	}
	if err := links.AppendDigest(ctx, lb.ID, &contracts.LinkDigest{Text: "backlog"}); err != nil {
		t.Fatal(err)
	}

	got, err := links.HeldUpdates(ctx, leader.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Held) != 2 || got.Discarded == nil || len(got.Discarded) != 0 {
		t.Fatalf("HeldUpdates = %+v", got)
	}
	h0, h1 := got.Held[0], got.Held[1]
	if h0.ID == 0 || h0.LinkID != la.ID || h0.SourceSessionID != a.ID || h0.SourceName != "A (snapshot)" ||
		h0.Kind != "digest" || h0.Text != "from a" || !h0.Noop || h0.CreatedAt.IsZero() {
		t.Errorf("held[0] = %+v", h0)
	}
	if h1.LinkID != lb.ID || h1.SourceSessionID != b.ID || h1.SourceName != "worker b" || h1.Text != "from b" || h1.Noop {
		t.Errorf("held[1] = %+v", h1)
	}
	if other, _ := links.HeldUpdates(ctx, a.ID); len(other.Held) != 0 {
		t.Errorf("the subject side must list nothing: %+v", other)
	}
}

func TestDiscardRestoreAndDrain(t *testing.T) {
	links, link := mkMailboxLink(t)
	ctx := context.Background()
	ids := held(t, links, link, 3, nil)
	if len(ids) != 3 {
		t.Fatalf("ids = %v", ids)
	}

	n, err := links.DiscardDigests(ctx, []int64{ids[1], 999999}, "user")
	if err != nil || n != 1 {
		t.Fatalf("DiscardDigests = %d, %v; want 1 (unknown id skipped)", n, err)
	}
	if n, _ := links.DiscardDigests(ctx, []int64{ids[1]}, "user"); n != 0 {
		t.Errorf("re-discard changed %d, want 0", n)
	}
	counts, _ := links.DeferredCounts(ctx)
	if counts[link.GrantingSessionID] != 2 {
		t.Errorf("DeferredCounts = %v, want 2 (discarded excluded)", counts)
	}
	list, _ := links.HeldUpdates(ctx, link.GrantingSessionID)
	if len(list.Held) != 2 || len(list.Discarded) != 1 || list.Discarded[0].ID != ids[1] || list.Discarded[0].Text != "h1" {
		t.Fatalf("after discard: %+v", list)
	}

	// Undo puts it back in its place.
	if n, err := links.RestoreDigests(ctx, []int64{ids[1]}, "user"); err != nil || n != 1 {
		t.Fatalf("RestoreDigests = %d, %v", n, err)
	}
	list, _ = links.HeldUpdates(ctx, link.GrantingSessionID)
	if len(list.Held) != 3 || list.Held[1].ID != ids[1] || len(list.Discarded) != 0 {
		t.Fatalf("after restore: %+v", list)
	}

	// A deferred drain never returns discarded rows, and deletes them.
	if _, err := links.DiscardDigests(ctx, []int64{ids[0]}, "user"); err != nil {
		t.Fatal(err)
	}
	drained, err := links.DrainDeferred(ctx, link.ID)
	if err != nil || len(drained) != 2 || drained[0].ID != ids[1] || drained[0].Text != "h1" || drained[1].Text != "h2" {
		t.Fatalf("DrainDeferred = %+v, %v; want h1, h2 with ids", drained, err)
	}
	list, _ = links.HeldUpdates(ctx, link.GrantingSessionID)
	if len(list.Held) != 0 || len(list.Discarded) != 0 {
		t.Errorf("after drain: %+v, want nothing left (discarded removed too)", list)
	}
	if n, _ := links.RestoreDigests(ctx, []int64{ids[0]}, "user"); n != 0 {
		t.Errorf("restore after drain changed %d, want 0", n)
	}
}

// Discarding is audited (one event per recipient) and fires the
// waiting-count session.updated; restore likewise.
func TestDiscardRestoreEvents(t *testing.T) {
	links, link := mkMailboxLink(t)
	ctx := context.Background()
	ids := held(t, links, link, 2, nil)
	before, _ := links.log.Query(ctx, Filter{SessionID: link.GrantingSessionID})
	since := before[len(before)-1].Seq

	if _, err := links.DiscardDigests(ctx, ids, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := links.RestoreDigests(ctx, ids[:1], "user"); err != nil {
		t.Fatal(err)
	}
	evs, err := links.log.Query(ctx, Filter{Since: since, SessionID: link.GrantingSessionID})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		types = append(types, string(e.Type))
	}
	want := []string{"link.digest_discarded", "session.updated", "link.digest_restored", "session.updated"}
	if len(types) != len(want) {
		t.Fatalf("events = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v, want %v", types, want)
		}
	}
	if evs[0].Actor != "user" || string(evs[0].Payload) == "" {
		t.Errorf("discard event = %+v", evs[0])
	}
}

// Discarded rows sit outside the per-class cap and are purged after the
// retention window on the next discard.
func TestDiscardedOutsideCapAndPurged(t *testing.T) {
	links, link := mkMailboxLink(t)
	ctx := context.Background()
	ids := held(t, links, link, 2, nil)
	if _, err := links.DiscardDigests(ctx, ids[:1], "user"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < linkDigestCap; i++ {
		if err := links.AppendDigest(ctx, link.ID, &contracts.LinkDigest{Text: "more", Deferred: true}); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := links.HeldUpdates(ctx, link.GrantingSessionID)
	if len(list.Held) != linkDigestCap || len(list.Discarded) != 1 {
		t.Fatalf("held %d discarded %d; want %d and 1", len(list.Held), len(list.Discarded), linkDigestCap)
	}

	old := time.Now().UTC().Add(-discardRetention - time.Hour).Format(discardedAtFormat)
	if _, err := links.log.db.ExecContext(ctx, `UPDATE link_digests SET discarded_at = ? WHERE id = ?`, old, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := links.DiscardDigests(ctx, []int64{list.Held[0].ID}, "user"); err != nil {
		t.Fatal(err)
	}
	list, _ = links.HeldUpdates(ctx, link.GrantingSessionID)
	if len(list.Discarded) != 1 || list.Discarded[0].ID == ids[0] {
		t.Errorf("discarded = %+v; the expired row should be purged, the fresh one kept", list.Discarded)
	}
}
