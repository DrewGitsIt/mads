package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestLinkForSessions(t *testing.T) {
	links, sessions, l := newLinkStore(t)
	ctx := context.Background()
	a := mkSession(t, sessions, contracts.Session{Name: "a"})
	b := mkSession(t, sessions, contracts.Session{Name: "b"})
	c := mkSession(t, sessions, contracts.Session{Name: "c"})
	d := mkSession(t, sessions, contracts.Session{Name: "d"})
	e := mkSession(t, sessions, contracts.Session{Name: "e"})

	mk := func(granting, subject string) contracts.Link {
		t.Helper()
		link := contracts.Link{GrantingSessionID: granting, SubjectSessionID: subject, Grants: []contracts.Grant{contracts.GrantRead}}
		if err := links.Create(ctx, &link, "user"); err != nil {
			t.Fatalf("create %s→%s: %v", granting, subject, err)
		}
		return link
	}
	// Created in this order, then created_at rewritten so the wanted order
	// (cToA, aToB, bToD) differs from insertion order and from id order.
	aToB := mk(a.ID, b.ID) // both endpoints requested: must appear once
	cToA := mk(c.ID, a.ID) // a as subject
	bToD := mk(b.ID, d.ID) // b as granting
	dToE := mk(d.ID, e.ID) // unrelated to {a, b}
	for id, ts := range map[string]string{
		cToA.ID: "2026-01-01T00:00:01Z",
		aToB.ID: "2026-01-01T00:00:02Z",
		bToD.ID: "2026-01-01T00:00:03Z",
		dToE.ID: "2026-01-01T00:00:00Z",
	} {
		if _, err := l.db.ExecContext(ctx, `UPDATE links SET created_at = ? WHERE id = ?`, ts, id); err != nil {
			t.Fatal(err)
		}
	}

	ids := func(list []contracts.Link) string {
		out := make([]string, len(list))
		for i, link := range list {
			out[i] = link.ID
		}
		return fmt.Sprint(out)
	}

	got, err := links.ForSessions(ctx, []string{a.ID, b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprint([]string{cToA.ID, aToB.ID, bToD.ID}); ids(got) != want {
		t.Errorf("ForSessions(a, b) = %s, want %s", ids(got), want)
	}

	got, err = links.ForSessions(ctx, []string{e.ID, "ses_nope"})
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprint([]string{dToE.ID}); ids(got) != want {
		t.Errorf("ForSessions(e, unknown) = %s, want %s", ids(got), want)
	}

	for _, in := range [][]string{nil, {}, {"ses_nope", "ses_also_nope"}} {
		got, err := links.ForSessions(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil || len(got) != 0 {
			t.Errorf("ForSessions(%v) = %#v, want empty non-nil", in, got)
		}
	}
}
