package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func mkMailboxLink(t *testing.T) (*Links, contracts.Link) {
	t.Helper()
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})
	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceOnSignal,
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	return links, link
}

func TestLinkDigestAppendDrainRoundTrip(t *testing.T) {
	links, link := mkMailboxLink(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if err := links.AppendDigest(ctx, link.ID, &contracts.LinkDigest{
			Text: fmt.Sprintf("digest %d", i), FromTurn: i, ToTurn: i,
		}); err != nil {
			t.Fatalf("AppendDigest %d: %v", i, err)
		}
	}

	got, err := links.DrainDigests(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("drained %d digests, want 3", len(got))
	}
	for i, d := range got {
		if want := fmt.Sprintf("digest %d", i+1); d.Text != want {
			t.Errorf("digest[%d].Text = %q, want %q (oldest first)", i, d.Text, want)
		}
		if d.FromTurn != i+1 || d.CreatedAt.IsZero() {
			t.Errorf("digest[%d] anchors/timestamp not round-tripped: %+v", i, d)
		}
	}

	// Drain clears: a second drain is empty.
	again, err := links.DrainDigests(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second drain returned %d digests, want 0", len(again))
	}
}

func TestLinkDigestAppendEnforcesCapOldestFirst(t *testing.T) {
	links, link := mkMailboxLink(t)
	ctx := context.Background()

	for i := 1; i <= linkDigestCap+5; i++ {
		if err := links.AppendDigest(ctx, link.ID, &contracts.LinkDigest{Text: fmt.Sprintf("digest %d", i)}); err != nil {
			t.Fatalf("AppendDigest %d: %v", i, err)
		}
	}
	got, err := links.DrainDigests(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != linkDigestCap {
		t.Fatalf("drained %d digests, want cap %d", len(got), linkDigestCap)
	}
	if want := fmt.Sprintf("digest %d", 6); got[0].Text != want {
		t.Errorf("oldest kept = %q, want %q (oldest dropped first)", got[0].Text, want)
	}
	if want := fmt.Sprintf("digest %d", linkDigestCap+5); got[len(got)-1].Text != want {
		t.Errorf("newest kept = %q, want %q", got[len(got)-1].Text, want)
	}
}

func TestLinkDigestAppendUnknownLinkErrors(t *testing.T) {
	links, _ := mkMailboxLink(t)
	err := links.AppendDigest(context.Background(), "lnk_nope", &contracts.LinkDigest{Text: "x"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("AppendDigest(unknown link) = %v, want ErrNotFound", err)
	}
}

func TestLinkDigestDrainUnknownLinkIsEmpty(t *testing.T) {
	links, _ := mkMailboxLink(t)
	got, err := links.DrainDigests(context.Background(), "lnk_nope")
	if err != nil {
		t.Fatalf("DrainDigests(unknown link) = %v, want nil error", err)
	}
	if len(got) != 0 {
		t.Fatalf("DrainDigests(unknown link) = %d digests, want 0", len(got))
	}
}

func TestLinkDeleteClearsMailbox(t *testing.T) {
	links, link := mkMailboxLink(t)
	ctx := context.Background()

	if err := links.AppendDigest(ctx, link.ID, &contracts.LinkDigest{Text: "orphan-to-be"}); err != nil {
		t.Fatal(err)
	}
	if err := links.Delete(ctx, link.ID, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := links.DrainDigests(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("mailbox survived link deletion: %d digests, want 0", len(got))
	}
}
