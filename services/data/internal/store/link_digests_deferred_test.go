package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestDeferredDigestsAreSeparateFromOnSignalBacklog(t *testing.T) {
	links, link := mkMailboxLink(t)
	ctx := context.Background()
	attr := &contracts.Attribution{LinkID: link.ID, SourceName: "src", DigestAttributed: true}

	if err := links.AppendDigest(ctx, link.ID, &contracts.LinkDigest{Text: "backlog"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < linkDigestCap+3; i++ {
		if err := links.AppendDigest(ctx, link.ID, &contracts.LinkDigest{
			Text: fmt.Sprintf("d%d", i), Deferred: true, Noop: true, Attribution: attr,
		}); err != nil {
			t.Fatal(err)
		}
	}

	counts, err := links.DeferredCounts(ctx)
	if err != nil || counts[link.GrantingSessionID] != linkDigestCap {
		t.Fatalf("DeferredCounts = %v, %v; want %d for the granting session (cap applies per class)", counts, err, linkDigestCap)
	}

	backlog, err := links.DrainDigests(ctx, link.ID)
	if err != nil || len(backlog) != 1 || backlog[0].Text != "backlog" || backlog[0].Deferred {
		t.Fatalf("DrainDigests = %+v, %v; want only the on_signal backlog", backlog, err)
	}
	deferred, err := links.DrainDeferred(ctx, link.ID)
	if err != nil || len(deferred) != linkDigestCap {
		t.Fatalf("DrainDeferred returned %d, %v; want %d", len(deferred), err, linkDigestCap)
	}
	if deferred[0].Text != "d3" || !deferred[0].Deferred || !deferred[0].Noop {
		t.Errorf("oldest kept = %+v, want d3 (oldest 3 dropped), deferred, noop", deferred[0])
	}
	if got := deferred[0].Attribution; got == nil || *got != *attr {
		t.Errorf("attribution = %+v, want %+v stored whole", got, attr)
	}
	if again, _ := links.DrainDeferred(ctx, link.ID); len(again) != 0 {
		t.Errorf("second DrainDeferred returned %d, want 0 (drain clears)", len(again))
	}
	if counts, _ := links.DeferredCounts(ctx); len(counts) != 0 {
		t.Errorf("DeferredCounts after drain = %v, want empty", counts)
	}
}
