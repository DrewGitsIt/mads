package store

import (
	"context"
	"errors"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func newRetroStore(t *testing.T) (*Retros, *Log) {
	t.Helper()
	l := openMigratedDB(t)
	return NewRetros(l), l
}

func mkRetro(t *testing.T, s *Retros, r contracts.Retro) contracts.Retro {
	t.Helper()
	if r.ID == "" {
		r.ID = NewID("rto")
	}
	if err := s.Create(context.Background(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRetroCreateAndGet(t *testing.T) {
	s, l := newRetroStore(t)
	ctx := context.Background()
	retro := mkRetro(t, s, contracts.Retro{
		SessionIDs: []string{"ses_1", "ses_2"},
		DPAs: []contracts.DPA{
			{ID: "dpa_1", Problem: "p", Evidence: "e", RootCause: "r",
				RemediationLayer: contracts.RemediationPromptEngineering, SuggestedAction: "a", Status: contracts.DPAAccepted},
		},
	})
	if retro.CreatedAt.IsZero() || retro.UpdatedAt.IsZero() {
		t.Fatalf("timestamps not stamped: %+v", retro)
	}

	got, err := s.Get(ctx, retro.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.DPAs) != 1 || got.DPAs[0].Problem != "p" {
		t.Errorf("got %+v", got)
	}

	types := eventTypes(t, l, Filter{Types: []string{"retro.completed"}})
	if len(types) != 1 {
		t.Errorf("retro.completed not announced: %v", types)
	}
}

func TestRetroCreateRequiresID(t *testing.T) {
	s, _ := newRetroStore(t)
	err := s.Create(context.Background(), &contracts.Retro{})
	var verr *contracts.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Create with empty ID: got %v, want *ValidationError", err)
	}
}

func TestRetroList(t *testing.T) {
	s, _ := newRetroStore(t)
	mkRetro(t, s, contracts.Retro{})
	mkRetro(t, s, contracts.Retro{})

	list, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("List: %d retros, want 2", len(list))
	}
}

func TestDPATransitions(t *testing.T) {
	s, l := newRetroStore(t)
	ctx := context.Background()
	retro := mkRetro(t, s, contracts.Retro{
		DPAs: []contracts.DPA{
			{ID: "dpa_1", Problem: "p1", Status: contracts.DPAAccepted},
			{ID: "dpa_2", Problem: "p2", Status: contracts.DPAAccepted},
		},
	})

	if err := s.AcceptDPA(ctx, retro.ID, "dpa_1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RejectDPA(ctx, retro.ID, "dpa_2"); err != nil {
		t.Fatal(err)
	}
	edited, err := s.EditDPA(ctx, retro.ID, "dpa_1", contracts.DPAEdit{Problem: "revised problem"})
	if err != nil {
		t.Fatal(err)
	}
	if edited.Problem != "revised problem" || edited.Status != contracts.DPAEdited {
		t.Errorf("EditDPA: %+v", edited)
	}

	got, err := s.Get(ctx, retro.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DPAs[0].Status != contracts.DPAEdited || got.DPAs[0].Problem != "revised problem" {
		t.Errorf("dpa_1 not persisted: %+v", got.DPAs[0])
	}
	if got.DPAs[1].Status != contracts.DPARejected {
		t.Errorf("dpa_2 not persisted: %+v", got.DPAs[1])
	}

	for _, want := range []string{"retro.dpa_accepted", "retro.dpa_rejected", "retro.dpa_edited"} {
		types := eventTypes(t, l, Filter{Types: []string{want}})
		if len(types) != 1 {
			t.Errorf("%s: %d events, want 1", want, len(types))
		}
	}
}

func TestDPANotFound(t *testing.T) {
	s, _ := newRetroStore(t)
	ctx := context.Background()
	retro := mkRetro(t, s, contracts.Retro{DPAs: []contracts.DPA{{ID: "dpa_1"}}})

	if err := s.AcceptDPA(ctx, retro.ID, "dpa_missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown dpa: got %v, want ErrNotFound", err)
	}
	if err := s.AcceptDPA(ctx, "rto_missing", "dpa_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown retro: got %v, want ErrNotFound", err)
	}
}

func TestSetFollowUpSession(t *testing.T) {
	s, l := newRetroStore(t)
	ctx := context.Background()
	retro := mkRetro(t, s, contracts.Retro{})

	if err := s.SetFollowUpSession(ctx, retro.ID, "ses_followup"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, retro.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FollowUpSessionID != "ses_followup" {
		t.Errorf("FollowUpSessionID = %q, want ses_followup", got.FollowUpSessionID)
	}
	types := eventTypes(t, l, Filter{Types: []string{"retro.follow_up_created"}})
	if len(types) != 1 {
		t.Errorf("retro.follow_up_created not announced: %v", types)
	}
}
