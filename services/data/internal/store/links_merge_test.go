package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Re-creating a link a pair already has, with the same grants, is an
// idempotent success: the existing link comes back, and there's no second
// row and no second link.created.
func TestLinkCreateIdenticalIsIdempotent(t *testing.T) {
	links, sessions, l := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	first := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: contracts.PresetGrants[contracts.PresetLeader], Cadence: contracts.CadencePerTurn,
	}
	if err := links.Create(ctx, &first, "user"); err != nil {
		t.Fatal(err)
	}

	// Same grants in a different order, cadence left unset: still identical.
	again := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantOrchestrate, contracts.GrantInject, contracts.GrantRead},
	}
	if err := links.Create(ctx, &again, "user"); err != nil {
		t.Fatalf("re-creating your own leader link: %v", err)
	}
	if again.ID != first.ID {
		t.Errorf("returned link id = %s, want the existing %s", again.ID, first.ID)
	}
	if again.Cadence != contracts.CadencePerTurn {
		t.Errorf("cadence = %s, want the existing per_turn kept when the request leaves it unset", again.Cadence)
	}
	if again.OrchestrateStatus != contracts.OrchestratePending {
		t.Errorf("orchestrate status = %q, want the stored pending_restart", again.OrchestrateStatus)
	}

	assertPairLinkCount(t, links, leader.ID, subject.ID, 1)
	types := eventTypes(t, l, Filter{SessionID: subject.ID, Types: []string{"link.*"}})
	if len(types) != 1 || types[0] != contracts.LinkCreated {
		t.Errorf("link events = %v, want exactly one link.created", types)
	}
}

// Leader over a subject you already monitor upgrades the monitor link in
// place: one link, same id, orchestrate added (pending_restart), and
// link.updated rather than a second link.created.
func TestLinkCreateUpgradesMonitorToLeaderInPlace(t *testing.T) {
	links, sessions, l := newLinkStore(t)
	ctx := context.Background()
	leader := mkSession(t, sessions, contracts.Session{Name: "leader"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	monitor := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: contracts.PresetGrants[contracts.PresetMonitor], Cadence: contracts.CadenceManualOnly,
	}
	if err := links.Create(ctx, &monitor, "user"); err != nil {
		t.Fatal(err)
	}

	upgrade := contracts.Link{
		GrantingSessionID: leader.ID, SubjectSessionID: subject.ID,
		Grants: contracts.PresetGrants[contracts.PresetLeader],
	}
	if err := links.Create(ctx, &upgrade, "user"); err != nil {
		t.Fatalf("monitor→leader upgrade: %v", err)
	}
	if upgrade.ID != monitor.ID {
		t.Errorf("upgraded link id = %s, want the monitor link's %s", upgrade.ID, monitor.ID)
	}
	if !hasGrant(upgrade.Grants, contracts.GrantOrchestrate) {
		t.Errorf("grants = %v, want orchestrate added", upgrade.Grants)
	}
	if upgrade.OrchestrateStatus != contracts.OrchestratePending {
		t.Errorf("orchestrate status = %q, want pending_restart", upgrade.OrchestrateStatus)
	}
	if upgrade.Cadence != contracts.CadenceManualOnly {
		t.Errorf("cadence = %s, want manual_only kept (the request left it unset)", upgrade.Cadence)
	}
	if !upgrade.CreatedAt.Equal(monitor.CreatedAt) {
		t.Errorf("created_at changed on an in-place upgrade")
	}

	stored, err := links.Get(ctx, monitor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !hasGrant(stored.Grants, contracts.GrantOrchestrate) {
		t.Errorf("stored grants = %v, want orchestrate", stored.Grants)
	}
	assertPairLinkCount(t, links, leader.ID, subject.ID, 1)
	types := eventTypes(t, l, Filter{SessionID: subject.ID, Types: []string{"link.*"}})
	if len(types) != 2 || types[0] != contracts.LinkCreated || types[1] != contracts.LinkUpdated {
		t.Errorf("link events = %v, want [link.created link.updated]", types)
	}
}

// A different grant set with an explicit cadence also updates the cadence.
func TestLinkCreateMergeAppliesRequestedCadence(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	a := mkSession(t, sessions, contracts.Session{Name: "a"})
	b := mkSession(t, sessions, contracts.Session{Name: "b"})
	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: a.ID, SubjectSessionID: b.ID,
		Grants: contracts.PresetGrants[contracts.PresetMonitor], Cadence: contracts.CadenceOnSignal,
	}, "user"); err != nil {
		t.Fatal(err)
	}
	// Same grants, different explicit cadence: an update, not a no-op.
	merged := contracts.Link{
		GrantingSessionID: a.ID, SubjectSessionID: b.ID,
		Grants: contracts.PresetGrants[contracts.PresetMonitor], Cadence: contracts.CadencePerTurn,
	}
	if err := links.Create(ctx, &merged, "user"); err != nil {
		t.Fatal(err)
	}
	if merged.Cadence != contracts.CadencePerTurn {
		t.Errorf("cadence = %s, want per_turn", merged.Cadence)
	}
	assertPairLinkCount(t, links, a.ID, b.ID, 1)
}

// An upgrade that would add orchestrate while another session already leads
// the subject is refused, and the message names that session by name and
// id. The monitor link is left untouched.
func TestLinkCreateUpgradeRefusedNamesOtherHolder(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	incumbent := mkSession(t, sessions, contracts.Session{Name: "release captain"})
	monitor := mkSession(t, sessions, contracts.Session{Name: "watcher"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})

	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: incumbent.ID, SubjectSessionID: subject.ID,
		Grants: contracts.PresetGrants[contracts.PresetLeader],
	}, "user"); err != nil {
		t.Fatal(err)
	}
	watch := contracts.Link{
		GrantingSessionID: monitor.ID, SubjectSessionID: subject.ID,
		Grants: contracts.PresetGrants[contracts.PresetMonitor],
	}
	if err := links.Create(ctx, &watch, "user"); err != nil {
		t.Fatal(err)
	}

	err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: monitor.ID, SubjectSessionID: subject.ID,
		Grants: contracts.PresetGrants[contracts.PresetLeader],
	}, "user")
	var verr *contracts.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("upgrade over another leader: err = %v, want *ValidationError", err)
	}
	msg := verr.Error()
	if !strings.Contains(msg, `"release captain"`) || !strings.Contains(msg, incumbent.ID) {
		t.Errorf("message %q doesn't name the holder (name and id)", msg)
	}

	stored, err := links.Get(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasGrant(stored.Grants, contracts.GrantOrchestrate) {
		t.Errorf("refused upgrade still changed the stored grants: %v", stored.Grants)
	}
}

// A fresh leader create over an already-led subject names the holder too.
func TestLinkCreateSecondHolderRefusalNamesHolder(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	incumbent := mkSession(t, sessions, contracts.Session{Name: "release captain"})
	challenger := mkSession(t, sessions, contracts.Session{Name: "challenger"})
	subject := mkSession(t, sessions, contracts.Session{Name: "subject"})
	if err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: incumbent.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantOrchestrate},
	}, "user"); err != nil {
		t.Fatal(err)
	}
	err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: challenger.ID, SubjectSessionID: subject.ID,
		Grants: []contracts.Grant{contracts.GrantOrchestrate},
	}, "user")
	var verr *contracts.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	if msg := verr.Error(); !strings.Contains(msg, `"release captain" (`+incumbent.ID+`)`) {
		t.Errorf("message %q doesn't name the holder as \"name\" (id)", msg)
	}
}

// However a pair is created or re-created, it ends up with one link.
func TestLinkCreateNeverAddsParallelPairLinks(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	a := mkSession(t, sessions, contracts.Session{Name: "a"})
	b := mkSession(t, sessions, contracts.Session{Name: "b"})
	for i, grants := range [][]contracts.Grant{
		contracts.PresetGrants[contracts.PresetMonitor],
		contracts.PresetGrants[contracts.PresetMonitor],
		contracts.PresetGrants[contracts.PresetLeader],
		contracts.PresetGrants[contracts.PresetMonitor], // downgrade
		{contracts.GrantRead, contracts.GrantInject},
	} {
		if err := links.Create(ctx, &contracts.Link{
			GrantingSessionID: a.ID, SubjectSessionID: b.ID, Grants: grants,
		}, "user"); err != nil {
			t.Fatalf("create %d (%v): %v", i, grants, err)
		}
		assertPairLinkCount(t, links, a.ID, b.ID, 1)
	}
}

func assertPairLinkCount(t *testing.T, links *Links, grantingID, subjectID string, want int) {
	t.Helper()
	all, err := links.ForSession(context.Background(), subjectID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range all {
		if l.GrantingSessionID == grantingID && l.SubjectSessionID == subjectID {
			n++
		}
	}
	if n != want {
		t.Fatalf("%s→%s has %d links, want %d", grantingID, subjectID, n, want)
	}
}
