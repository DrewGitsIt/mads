package store

import (
	"context"
	"errors"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func newLinkStore(t *testing.T) (*Links, *Sessions, *Log) {
	t.Helper()
	l := openMigratedDB(t)
	sessions := NewSessions(l)
	return NewLinks(l, sessions), sessions, l
}

func TestLinkRoundTrip(t *testing.T) {
	links, sessions, l := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID,
		SubjectSessionID:  subject.ID,
		Grants:            []contracts.Grant{contracts.GrantRead},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	if link.ID == "" {
		t.Fatal("no ID assigned")
	}

	got, err := links.Get(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Grants) != 1 || got.Grants[0] != contracts.GrantRead {
		t.Errorf("grants = %v, want [read]", got.Grants)
	}

	link.Grants = []contracts.Grant{contracts.GrantRead, contracts.GrantInject}
	if err := links.Update(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	got, err = links.Get(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Grants) != 2 {
		t.Errorf("grants after update = %v, want [read inject]", got.Grants)
	}

	// ForSession returns the link from either endpoint's perspective.
	fromLeader, err := links.ForSession(ctx, leader.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromLeader) != 1 || fromLeader[0].ID != link.ID {
		t.Errorf("ForSession(leader) = %v, want [%s]", fromLeader, link.ID)
	}
	fromSubject, err := links.ForSession(ctx, subject.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fromSubject) != 1 || fromSubject[0].ID != link.ID {
		t.Errorf("ForSession(subject) = %v, want [%s]", fromSubject, link.ID)
	}

	if err := links.Delete(ctx, link.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := links.Get(ctx, link.ID); !errors.Is(err, ErrNotFound) {
		t.Error("link survived delete")
	}

	types := eventTypes(t, l, Filter{SessionID: subject.ID, Types: []string{"link.*"}})
	want := []contracts.EventType{contracts.LinkCreated, contracts.LinkUpdated, contracts.LinkDeleted}
	if len(types) != len(want) {
		t.Fatalf("events %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("event %d: %s, want %s", i, types[i], want[i])
		}
	}
}

func TestLinkForSessionEmptyForUnknownSession(t *testing.T) {
	links, _, _ := newLinkStore(t)
	got, err := links.ForSession(context.Background(), "ses_nope")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("ForSession(unknown) = %v, want empty", got)
	}
}

func TestLinkCreateRequiresBothSessionsExist(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})

	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: "ses_nope", SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}, "user"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown granting session: err = %v, want ErrNotFound", err)
	}

	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: "ses_nope",
		Grants: []contracts.Grant{contracts.GrantRead},
	}, "user"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown subject session: err = %v, want ErrNotFound", err)
	}
}

func TestLinkCreateRefusesEmptyOrInvalidGrants(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	var verr *contracts.ValidationError
	err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
	}, "user")
	if !errors.As(err, &verr) {
		t.Fatalf("empty grants: err = %v, want *ValidationError", err)
	}

	err = links.Create(ctx, &contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{"bogus"},
	}, "user")
	if !errors.As(err, &verr) {
		t.Fatalf("invalid grant: err = %v, want *ValidationError", err)
	}
}

func TestLinkCreateRefusesSelfLink(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	sess := mkSession(t, sessions, contracts.Session{Name: "solo"})

	var verr *contracts.ValidationError
	err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: sess.ID, SubjectSessionID: sess.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}, "user")
	if !errors.As(err, &verr) {
		t.Fatalf("self-link: err = %v, want *ValidationError", err)
	}
}

func TestLinkCreateRefusesReverseDirection(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	a := mkSession(t, sessions, contracts.Session{Name: "a"})
	b := mkSession(t, sessions, contracts.Session{Name: "b"})

	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: a.ID, SubjectSessionID: b.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}, "user"); err != nil {
		t.Fatal(err)
	}

	var verr *contracts.ValidationError
	err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: b.ID, SubjectSessionID: a.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}, "user")
	if !errors.As(err, &verr) {
		t.Fatalf("reverse direction: err = %v, want *ValidationError", err)
	}
}

func TestLinkCreateRefusesDifferentWorktreesSameWorkspace(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	a := mkSession(t, sessions, contracts.Session{Name: "a", Workspace: "mads-core", WorktreeDir: "/root/ses_a"})
	b := mkSession(t, sessions, contracts.Session{Name: "b", Workspace: "mads-core", WorktreeDir: "/root/ses_b"})

	var verr *contracts.ValidationError
	err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: a.ID, SubjectSessionID: b.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}, "user")
	if !errors.As(err, &verr) {
		t.Fatalf("different worktrees, same workspace: err = %v, want *ValidationError", err)
	}
}

// Control case: different workspaces (or no worktree isolation at all) are
// not caught by the worktree constraint — it only fires for the same
// workspace with differing worktree dirs.
func TestLinkCreateAllowsDifferentWorkspaces(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	a := mkSession(t, sessions, contracts.Session{Name: "a", Workspace: "mads-core", WorktreeDir: "/root/ses_a"})
	b := mkSession(t, sessions, contracts.Session{Name: "b", Workspace: "mads-ui", WorktreeDir: "/root/ses_b"})

	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: a.ID, SubjectSessionID: b.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}, "user"); err != nil {
		t.Fatalf("different workspaces should not be refused: %v", err)
	}
}

func TestLinkCreateRefusesOrchestrateOnLivePipelineSubject(t *testing.T) {
	links, sessions, l := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	if err := l.Append(ctx, &contracts.Envelope{
		Type: contracts.PipelineStarted, SessionID: subject.ID, Actor: "pipeline",
	}); err != nil {
		t.Fatal(err)
	}

	var verr *contracts.ValidationError
	err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead, contracts.GrantOrchestrate},
	}, "user")
	if !errors.As(err, &verr) {
		t.Fatalf("orchestrate on live pipeline subject: err = %v, want *ValidationError", err)
	}

	// read-only stays fine even with a live pipeline (monitorable, not leadable).
	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}, "user"); err != nil {
		t.Fatalf("read-only link on live pipeline subject should be allowed: %v", err)
	}
}

func TestLinkCreateAllowsOrchestrateAfterPipelineCompletes(t *testing.T) {
	links, sessions, l := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	if err := l.Append(ctx, &contracts.Envelope{
		Type: contracts.PipelineStarted, SessionID: subject.ID, Actor: "pipeline",
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(ctx, &contracts.Envelope{
		Type: contracts.PipelineCompleted, SessionID: subject.ID, Actor: "pipeline",
	}); err != nil {
		t.Fatal(err)
	}

	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantOrchestrate},
	}, "user"); err != nil {
		t.Fatalf("orchestrate after pipeline completed should be allowed: %v", err)
	}
}

func TestLinkCreateRefusesSecondOrchestrateHolder(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leaderA := mkSession(t, sessions, contracts.Session{Name: "leaderA"})
	leaderB := mkSession(t, sessions, contracts.Session{Name: "leaderB"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: leaderA.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantOrchestrate},
	}, "user"); err != nil {
		t.Fatal(err)
	}

	var verr *contracts.ValidationError
	err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: leaderB.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantOrchestrate},
	}, "user")
	if !errors.As(err, &verr) {
		t.Fatalf("second orchestrate holder: err = %v, want *ValidationError", err)
	}
}

// Updating an existing orchestrate-holding link's own grants must not trip
// over its own row when checking the single-holder rule.
func TestLinkUpdateOrchestrateExcludesOwnRow(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantOrchestrate},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}

	link.Grants = []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate}
	if err := links.Update(ctx, &link, "user"); err != nil {
		t.Fatalf("re-saving the sole orchestrate holder should not trip the single-holder rule: %v", err)
	}
}

func TestLinkUpdateRefusesSecondOrchestrateHolder(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leaderA := mkSession(t, sessions, contracts.Session{Name: "leaderA"})
	leaderB := mkSession(t, sessions, contracts.Session{Name: "leaderB"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: leaderA.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantOrchestrate},
	}, "user"); err != nil {
		t.Fatal(err)
	}
	monitor := contracts.Link{
		GrantingSessionID: leaderB.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}
	if err := links.Create(ctx, &monitor, "user"); err != nil {
		t.Fatal(err)
	}

	monitor.Grants = []contracts.Grant{contracts.GrantRead, contracts.GrantOrchestrate}
	var verr *contracts.ValidationError
	if err := links.Update(ctx, &monitor, "user"); !errors.As(err, &verr) {
		t.Fatalf("update to second orchestrate holder: err = %v, want *ValidationError", err)
	}
}

func TestLinkSubjectCanHaveManyMonitors(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})
	for i := 0; i < 3; i++ {
		monitor := mkSession(t, sessions, contracts.Session{Name: "monitor"})
		if err := links.Create(ctx, &contracts.Link{
			GrantingSessionID: monitor.ID, SubjectSessionID: subject.ID,
			Grants: []contracts.Grant{contracts.GrantRead},
		}, "user"); err != nil {
			t.Fatalf("monitor %d: %v", i, err)
		}
	}
	got, err := links.ForSession(ctx, subject.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("ForSession(subject) = %d links, want 3", len(got))
	}
}
