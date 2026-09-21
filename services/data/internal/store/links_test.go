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

func TestLinkCreateDefaultsCadenceToOnSignal(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	if link.Cadence != contracts.CadenceOnSignal {
		t.Errorf("cadence = %q, want on_signal", link.Cadence)
	}

	got, err := links.Get(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cadence != contracts.CadenceOnSignal {
		t.Errorf("stored cadence = %q, want on_signal", got.Cadence)
	}
}

func TestLinkCreateRoundTripsManualOnlyCadence(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadenceManualOnly,
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	got, err := links.Get(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cadence != contracts.CadenceManualOnly {
		t.Errorf("stored cadence = %q, want manual_only", got.Cadence)
	}
}

func TestLinkCreateRefusesInvalidCadence(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	var verr *contracts.ValidationError
	err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: "bogus",
	}, "user")
	if !errors.As(err, &verr) {
		t.Fatalf("invalid cadence: err = %v, want *ValidationError", err)
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

func TestCreateImplicitHandRaiser(t *testing.T) {
	links, sessions, l := newLinkStore(t)
	ctx := context.Background()
	sess := mkSession(t, sessions, contracts.Session{Name: "solo"})

	link, err := links.CreateImplicitHandRaiser(ctx, sess.ID, "system")
	if err != nil {
		t.Fatal(err)
	}
	if link.GrantingSessionID != sess.ID || link.SubjectSessionID != sess.ID {
		t.Fatalf("self-referential link: granting=%s subject=%s, want both %s", link.GrantingSessionID, link.SubjectSessionID, sess.ID)
	}
	if !link.Implicit {
		t.Error("Implicit = false, want true")
	}
	want := contracts.PresetGrants[contracts.PresetHandRaiser]
	if len(link.Grants) != len(want) {
		t.Errorf("grants = %v, want %v", link.Grants, want)
	}

	got, err := links.Get(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Implicit {
		t.Error("stored Implicit = false, want true")
	}

	types := eventTypes(t, l, Filter{SessionID: sess.ID, Types: []string{"link.*"}})
	if len(types) != 1 || types[0] != contracts.LinkCreated {
		t.Errorf("events = %v, want [link.created]", types)
	}
}

func TestCreateImplicitHandRaiserIdempotent(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	sess := mkSession(t, sessions, contracts.Session{Name: "solo"})

	first, err := links.CreateImplicitHandRaiser(ctx, sess.ID, "system")
	if err != nil {
		t.Fatal(err)
	}
	second, err := links.CreateImplicitHandRaiser(ctx, sess.ID, "system")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Errorf("second call created a new link %s, want the same one %s", second.ID, first.ID)
	}

	got, err := links.ForSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("ForSession = %d links, want 1 (no duplicate)", len(got))
	}
}

func TestRemoveImplicitHandRaiser(t *testing.T) {
	links, sessions, l := newLinkStore(t)
	ctx := context.Background()
	sess := mkSession(t, sessions, contracts.Session{Name: "solo"})

	link, err := links.CreateImplicitHandRaiser(ctx, sess.ID, "system")
	if err != nil {
		t.Fatal(err)
	}
	if err := links.RemoveImplicitHandRaiser(ctx, sess.ID, "system"); err != nil {
		t.Fatal(err)
	}
	if _, err := links.Get(ctx, link.ID); !errors.Is(err, ErrNotFound) {
		t.Error("implicit hand-raiser survived removal")
	}

	types := eventTypes(t, l, Filter{SessionID: sess.ID, Types: []string{"link.*"}})
	want := []contracts.EventType{contracts.LinkCreated, contracts.LinkDeleted}
	if len(types) != len(want) {
		t.Fatalf("events %v, want %v", types, want)
	}
}

func TestRemoveImplicitHandRaiserNoOpWhenAbsent(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	sess := mkSession(t, sessions, contracts.Session{Name: "solo"})

	if err := links.RemoveImplicitHandRaiser(ctx, sess.ID, "system"); err != nil {
		t.Fatalf("removing absent implicit hand-raiser should be a no-op, got: %v", err)
	}
}

func TestLinkCreateSetsOrchestratePending(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	if link.OrchestrateStatus != contracts.OrchestratePending {
		t.Errorf("OrchestrateStatus = %q, want %q", link.OrchestrateStatus, contracts.OrchestratePending)
	}
	got, err := links.Get(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OrchestrateStatus != contracts.OrchestratePending {
		t.Errorf("stored OrchestrateStatus = %q, want %q", got.OrchestrateStatus, contracts.OrchestratePending)
	}
}

func TestLinkCreateWithoutOrchestrateGrantLeavesStatusEmpty(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	if link.OrchestrateStatus != "" {
		t.Errorf("OrchestrateStatus = %q, want empty (no orchestrate grant)", link.OrchestrateStatus)
	}
}

func TestLinkUpdatePreservesOrchestrateStatusAcrossGrantPreservingUpdate(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := links.SetOrchestrateStatus(ctx, link.ID, contracts.OrchestrateActive, "system"); err != nil {
		t.Fatal(err)
	}

	link.Cadence = contracts.CadenceManualOnly
	if err := links.Update(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	if link.OrchestrateStatus != contracts.OrchestrateActive {
		t.Errorf("OrchestrateStatus after grant-preserving update = %q, want %q", link.OrchestrateStatus, contracts.OrchestrateActive)
	}
}

func TestLinkUpdateResetsOrchestrateStatusWhenGrantNewlyAdded(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}

	link.Grants = []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate}
	if err := links.Update(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	if link.OrchestrateStatus != contracts.OrchestratePending {
		t.Errorf("OrchestrateStatus after adding the grant = %q, want %q", link.OrchestrateStatus, contracts.OrchestratePending)
	}
}

func TestLinkUpdateClearsOrchestrateStatusWhenGrantRemoved(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	if _, err := links.SetOrchestrateStatus(ctx, link.ID, contracts.OrchestrateActive, "system"); err != nil {
		t.Fatal(err)
	}

	link.Grants = []contracts.Grant{contracts.GrantRead}
	if err := links.Update(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	if link.OrchestrateStatus != "" {
		t.Errorf("OrchestrateStatus after removing the grant = %q, want empty", link.OrchestrateStatus)
	}
	got, err := links.Get(ctx, link.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OrchestrateStatus != "" {
		t.Errorf("stored OrchestrateStatus after removal = %q, want empty", got.OrchestrateStatus)
	}
}

func TestSetOrchestrateStatusRejectsLinkWithoutGrant(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	_, err := links.SetOrchestrateStatus(ctx, link.ID, contracts.OrchestrateActive, "system")
	var v *contracts.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("SetOrchestrateStatus on grant-less link: err = %v, want *ValidationError", err)
	}
}

func TestSetOrchestrateStatusRejectsUnknownValue(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	_, err := links.SetOrchestrateStatus(ctx, link.ID, contracts.OrchestrateStatus("bogus"), "system")
	var v *contracts.ValidationError
	if !errors.As(err, &v) {
		t.Fatalf("SetOrchestrateStatus(bogus): err = %v, want *ValidationError", err)
	}
}

func TestSetOrchestrateStatusNoopWhenUnchanged(t *testing.T) {
	links, sessions, l := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	link := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate},
	}
	if err := links.Create(ctx, &link, "user"); err != nil {
		t.Fatal(err)
	}
	// Already pending_restart from Create — setting the same value again
	// must not emit a redundant link.updated envelope.
	if _, err := links.SetOrchestrateStatus(ctx, link.ID, contracts.OrchestratePending, "system"); err != nil {
		t.Fatal(err)
	}
	types := eventTypes(t, l, Filter{SessionID: subject.ID, Types: []string{"link.*"}})
	want := []contracts.EventType{contracts.LinkCreated}
	if len(types) != len(want) {
		t.Fatalf("events %v, want %v", types, want)
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
