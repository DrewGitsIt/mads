package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

var (
	leaderGrants  = contracts.PresetGrants[contracts.PresetLeader]
	monitorGrants = contracts.PresetGrants[contracts.PresetMonitor]
)

func mustLink(t *testing.T, links *Links, granting, subject string, grants []contracts.Grant) contracts.Link {
	t.Helper()
	link := contracts.Link{GrantingSessionID: granting, SubjectSessionID: subject, Grants: grants}
	if err := links.Create(context.Background(), &link, "user"); err != nil {
		t.Fatalf("link %s → %s (%v): %v", granting, subject, grants, err)
	}
	return link
}

// wantLoopRefusal asserts err is a ValidationError on "grants" whose message
// spells out loop, a chain of sessions, as "name" (id) labels.
func wantLoopRefusal(t *testing.T, err error, loop ...contracts.Session) {
	t.Helper()
	var verr *contracts.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want *ValidationError", err)
	}
	labels := make([]string, len(loop))
	for i, s := range loop {
		labels[i] = holderLabel(s.ID, s.Name)
	}
	want := "this would make a loop of leaders: " + strings.Join(labels, " → ")
	for _, fe := range verr.Errors {
		if fe.Field == "grants" && strings.Contains(fe.Message, want) {
			return
		}
	}
	t.Errorf("errors = %+v, want a grants error containing %q", verr.Errors, want)
}

func threeSessions(t *testing.T, sessions *Sessions) (a, b, c contracts.Session) {
	t.Helper()
	return mkSession(t, sessions, contracts.Session{Name: "Release captain"}),
		mkSession(t, sessions, contracts.Session{Name: "Fix deploy"}),
		mkSession(t, sessions, contracts.Session{Name: "Mergiraf"})
}

// A leads B, B leads C: C leading A would close a loop, and the refusal
// walks it starting from the would-be leader.
func TestLinkCreateRefusesLeaderLoop(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	a, b, c := threeSessions(t, sessions)
	mustLink(t, links, a.ID, b.ID, leaderGrants)
	mustLink(t, links, b.ID, c.ID, leaderGrants)

	err := links.Create(context.Background(), &contracts.Link{
		GrantingSessionID: c.ID, SubjectSessionID: a.ID, Grants: leaderGrants,
	}, "user")
	wantLoopRefusal(t, err, c, a, b, c)
	assertPairLinkCount(t, links, c.ID, a.ID, 0)
}

// Re-creating an existing monitor pair as a leader merges into it; when
// that would close a loop it is refused and the monitor link is untouched.
func TestLinkCreateMergeRefusesLeaderLoop(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	a, b, c := threeSessions(t, sessions)
	mustLink(t, links, a.ID, b.ID, leaderGrants)
	mustLink(t, links, b.ID, c.ID, leaderGrants)
	watch := mustLink(t, links, c.ID, a.ID, monitorGrants)

	err := links.Create(ctx, &contracts.Link{
		GrantingSessionID: c.ID, SubjectSessionID: a.ID, Grants: leaderGrants,
	}, "user")
	wantLoopRefusal(t, err, c, a, b, c)
	stored, err := links.Get(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasGrant(stored.Grants, contracts.GrantOrchestrate) {
		t.Errorf("refused merge still changed the stored grants: %v", stored.Grants)
	}
}

// Upgrading a monitor link to leader through Update is refused the same way.
func TestLinkUpdateRefusesLeaderLoop(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	a, b, c := threeSessions(t, sessions)
	mustLink(t, links, a.ID, b.ID, leaderGrants)
	mustLink(t, links, b.ID, c.ID, leaderGrants)
	watch := mustLink(t, links, c.ID, a.ID, monitorGrants)

	upgrade := watch
	upgrade.Grants = leaderGrants
	wantLoopRefusal(t, links.Update(ctx, &upgrade, "user"), c, a, b, c)
	stored, err := links.Get(ctx, watch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if hasGrant(stored.Grants, contracts.GrantOrchestrate) {
		t.Errorf("refused upgrade still changed the stored grants: %v", stored.Grants)
	}
}

// Monitors only read, so a loop of monitors is fine.
func TestLinkCreateAllowsMonitorLoop(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	a, b, c := threeSessions(t, sessions)
	mustLink(t, links, a.ID, b.ID, monitorGrants)
	mustLink(t, links, b.ID, c.ID, monitorGrants)
	mustLink(t, links, c.ID, a.ID, monitorGrants)
}

// A loop with any monitor edge in it isn't a loop of leaders.
func TestLinkCreateAllowsMixedLoop(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	a, b, c := threeSessions(t, sessions)
	mustLink(t, links, a.ID, b.ID, leaderGrants)
	mustLink(t, links, b.ID, c.ID, monitorGrants)
	mustLink(t, links, c.ID, a.ID, leaderGrants)
}

// A plain chain of leaders is allowed, including a second branch.
func TestLinkCreateAllowsLeaderChain(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	a, b, c := threeSessions(t, sessions)
	d := mkSession(t, sessions, contracts.Session{Name: "d"})
	mustLink(t, links, a.ID, b.ID, leaderGrants)
	mustLink(t, links, b.ID, c.ID, leaderGrants)
	mustLink(t, links, a.ID, d.ID, leaderGrants)
	mustLink(t, links, d.ID, c.ID, monitorGrants)
}

// A loop stored before the rule existed (written here straight to the
// table) must not trap its links: a cadence-only edit and a downgrade both
// go through, since neither adds a leader.
func TestLinkUpdateAllowedOnLegacyLeaderLoop(t *testing.T) {
	links, sessions, l := newLinkStore(t)
	ctx := context.Background()
	a, b, c := threeSessions(t, sessions)
	var loop []contracts.Link
	if err := l.WithTx(ctx, func(tx *Tx) error {
		now := time.Now().UTC()
		for _, pair := range [][2]string{{a.ID, b.ID}, {b.ID, c.ID}, {c.ID, a.ID}} {
			link := contracts.Link{
				ID: NewID("lnk"), GrantingSessionID: pair[0], SubjectSessionID: pair[1],
				Grants: leaderGrants, Cadence: contracts.CadenceOnSignal,
				OrchestrateStatus: contracts.OrchestratePending, CreatedAt: now, UpdatedAt: now,
			}
			if err := writeLink(tx, &link); err != nil {
				return err
			}
			loop = append(loop, link)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cadence := loop[0]
	cadence.Cadence = contracts.CadencePerTurn
	if err := links.Update(ctx, &cadence, "user"); err != nil {
		t.Fatalf("cadence-only edit on a legacy loop: %v", err)
	}
	downgrade := loop[2]
	downgrade.Grants = monitorGrants
	if err := links.Update(ctx, &downgrade, "user"); err != nil {
		t.Fatalf("downgrade on a legacy loop: %v", err)
	}
	// With the loop broken, upgrading it back would re-close it.
	downgrade.Grants = leaderGrants
	wantLoopRefusal(t, links.Update(ctx, &downgrade, "user"), c, a, b, c)
}

// Staged links for a not-yet-created session: it leads A and is led by C,
// and A already leads C through B. The later entry is refused, naming the
// new session in the loop.
func TestValidateStagedRefusesLeaderLoop(t *testing.T) {
	links, sessions, _ := newLinkStore(t)
	ctx := context.Background()
	a, b, c := threeSessions(t, sessions)
	mustLink(t, links, a.ID, b.ID, leaderGrants)
	mustLink(t, links, b.ID, c.ID, leaderGrants)
	newSession := contracts.NewSessionPlaceholder

	leadsA := StagedLinkCheck{Field: "links[0]", Link: contracts.Link{
		GrantingSessionID: newSession, SubjectSessionID: a.ID, Grants: leaderGrants,
	}}
	ledByC := StagedLinkCheck{Field: "links[1]", Link: contracts.Link{
		GrantingSessionID: c.ID, SubjectSessionID: newSession, Grants: leaderGrants,
	}}
	labels := []string{"the new session", holderLabel(a.ID, a.Name), holderLabel(b.ID, b.Name), holderLabel(c.ID, c.Name), "the new session"}
	wantMsg := "this would make a loop of leaders: " + strings.Join(labels, " → ")

	for _, tc := range []struct {
		name   string
		checks []StagedLinkCheck
		field  string
	}{
		{"leader last", []StagedLinkCheck{leadsA, ledByC}, "links[1].grants"},
		{"leader first", []StagedLinkCheck{ledByC, leadsA}, "links[1].grants"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := make([]StagedLinkCheck, len(tc.checks))
			copy(checks, tc.checks)
			for i := range checks {
				checks[i].Field = []string{"links[0]", "links[1]"}[i]
			}
			verr, err := links.ValidateStaged(ctx, checks)
			if err != nil {
				t.Fatal(err)
			}
			if verr == nil || len(verr.Errors) != 1 || verr.Errors[0].Field != tc.field ||
				!strings.Contains(verr.Errors[0].Message, wantMsg) {
				t.Fatalf("errors = %+v, want one on %s containing %q", verr, tc.field, wantMsg)
			}
		})
	}

	// Either half alone, or the new session monitoring instead of leading,
	// is fine.
	monitorsA := leadsA
	monitorsA.Link.Grants = monitorGrants
	for name, checks := range map[string][]StagedLinkCheck{
		"leads A only":  {leadsA},
		"led by C only": {{Field: "links[0]", Link: ledByC.Link}},
		"monitors A":    {monitorsA, ledByC},
	} {
		if verr, err := links.ValidateStaged(ctx, checks); err != nil || verr != nil {
			t.Errorf("%s: verr = %+v, err = %v, want none", name, verr, err)
		}
	}
}
