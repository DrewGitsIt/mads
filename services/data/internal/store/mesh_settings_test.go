package store

import (
	"encoding/json"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// MeshSettings reads only the store's own writes, and an unreadable one
// falls back to the next older readable one instead of failing (A, B).
func TestMeshSettingsSkipsUnreadableAndForeign(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db, Migrations()); err != nil {
		t.Fatal(err)
	}
	l := NewLog(db)
	ctx := t.Context()
	off, on := false, true
	if err := l.SetMeshSettings(ctx, contracts.MeshSettings{RequireTailnet: &off}, "user"); err != nil {
		t.Fatal(err)
	}
	// Newer, but not readable as a setting, or not the store's.
	for _, env := range []contracts.Envelope{
		{Type: contracts.DeviceSettingsChanged, Actor: MeshSettingsActor, Payload: json.RawMessage(`"garbage"`)},
		{Type: contracts.DeviceSettingsChanged, Actor: MeshSettingsActor, Payload: json.RawMessage(`{}`)},
		{Type: contracts.DeviceSettingsChanged, Actor: "user", Payload: json.RawMessage(`{"require_tailnet":true}`)},
	} {
		if err := l.Append(ctx, &env); err != nil {
			t.Fatal(err)
		}
	}
	s, err := l.MeshSettings(ctx)
	if err != nil || s.RequireTailnet == nil || *s.RequireTailnet {
		t.Fatalf("MeshSettings = %+v, %v; want the last readable one (off)", s, err)
	}
	if err := l.SetMeshSettings(ctx, contracts.MeshSettings{RequireTailnet: &on}, "user"); err != nil {
		t.Fatal(err)
	}
	if s, _ := l.MeshSettings(ctx); s.RequireTailnet == nil || !*s.RequireTailnet {
		t.Fatalf("newest readable wins: %+v", s)
	}
}
