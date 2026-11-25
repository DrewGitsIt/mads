package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func migratedLog(t *testing.T) (*sql.DB, *Log) {
	t.Helper()
	db := openTestDB(t)
	if err := Migrate(db, Migrations()); err != nil {
		t.Fatal(err)
	}
	return db, NewLog(db)
}

// The row and its device.settings_changed are written in one transaction:
// the row is the setting, the event its history (and the Mesh Service's
// signal).
func TestMeshSettingsRowAndEvent(t *testing.T) {
	_, l := migratedLog(t)
	ctx := t.Context()
	s, err := l.MeshSettings(ctx)
	if err != nil || len(s.Networks) != 0 {
		t.Fatalf("fresh: %+v %v", s, err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	in := contracts.MeshSettings{Networks: []contracts.NetworkChoice{{ID: "tailscale", Listen: true, TailnetCheck: true, DecidedAt: now}}}
	out, err := l.SetMeshSettings(ctx, in, "user")
	if err != nil || out.RequireTailnet == nil || !*out.RequireTailnet {
		t.Fatalf("set: %+v %v", out, err)
	}
	got, _ := l.MeshSettings(ctx)
	if !got.TailnetCheck() || len(got.Chosen()) != 1 || got.MeshPort() != 17380 {
		t.Fatalf("read back: %+v", got)
	}
	evs, err := l.DeviceActivity(ctx, DeviceActivityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	for _, e := range evs {
		if e.Type == contracts.DeviceSettingsChanged {
			n++
			var c contracts.MeshSettingsChange
			_ = json.Unmarshal(e.Payload, &c)
			if e.Actor != MeshSettingsActor || c.By != "user" || len(c.Networks) != 1 || c.RequireTailnet == nil || !*c.RequireTailnet {
				t.Errorf("event %s by %q: %+v", e.Actor, c.By, c)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d events", n)
	}
	// A LAN-only choice keeps require_tailnet on for a rollback (an older
	// build would still listen on the tailnet only: check it there).
	out, err = l.SetMeshSettings(ctx, contracts.MeshSettings{Networks: []contracts.NetworkChoice{{ID: "lan:192.168.1.0/24/192.168.1.1", Listen: true}}}, "user")
	if err != nil || *out.RequireTailnet != true || out.TailnetCheck() {
		t.Fatalf("lan: %+v %v", out, err)
	}
}

// A refused write leaves neither the row nor an event.
func TestMeshSettingsValidation(t *testing.T) {
	_, l := migratedLog(t)
	ctx := t.Context()
	for name, s := range map[string]contracts.MeshSettings{
		"check off tailscale": {Networks: []contracts.NetworkChoice{{ID: "lan:x", Listen: true, TailnetCheck: true}}},
		"twice":               {Networks: []contracts.NetworkChoice{{ID: "tailscale"}, {ID: "tailscale"}}},
		"no id":               {Networks: []contracts.NetworkChoice{{Listen: true}}},
		"spaces":              {Networks: []contracts.NetworkChoice{{ID: "lan x"}}},
		"low port":            {Port: 80},
		"two networks":        {Networks: []contracts.NetworkChoice{{ID: "tailscale", Listen: true}, {ID: "lan:x", Listen: true}}},
		"public":              {Networks: []contracts.NetworkChoice{{ID: "public:en0", Listen: true}}},
	} {
		_, err := l.SetMeshSettings(ctx, s, "user")
		var v *contracts.ValidationError
		if !errors.As(err, &v) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s, _ := l.MeshSettings(ctx); len(s.Networks) != 0 {
		t.Fatalf("a refused write stored %+v", s)
	}
}

// Migration 0038 builds the row from what the machine ran with.
func TestMigration0038(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, db *sql.DB)
		// want: "" none chosen; "check" tailscale with the check; "nocheck".
		want string
	}{
		{"fresh install", func(*testing.T, *sql.DB) {}, ""},
		{"never set, Mesh Service ran", func(t *testing.T, db *sql.DB) {
			insertEvent(t, db, "mesh.listening", "mesh", `{"node":"pop-os","mesh_url":"http://pop-os.ts.net:17380"}`)
		}, "check"},
		{"never set, a paired device", func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`INSERT INTO mesh_peers (id, name, system, data, created_at, updated_at) VALUES ('per_1','pop-os',0,'{}','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
				t.Fatal(err)
			}
		}, "check"},
		{"set true", func(t *testing.T, db *sql.DB) {
			insertEvent(t, db, "device.settings_changed", "settings", `{"require_tailnet":true,"by":"user"}`)
		}, "check"},
		{"set false", func(t *testing.T, db *sql.DB) {
			insertEvent(t, db, "device.settings_changed", "settings", `{"require_tailnet":false,"by":"user"}`)
		}, "nocheck"},
		{"newest readable wins", func(t *testing.T, db *sql.DB) {
			insertEvent(t, db, "device.settings_changed", "settings", `{"require_tailnet":true}`)
			insertEvent(t, db, "device.settings_changed", "settings", `{"require_tailnet":false}`)
			insertEvent(t, db, "device.settings_changed", "settings", `"garbage"`)
			insertEvent(t, db, "device.settings_changed", "settings", `{}`)
			insertEvent(t, db, "device.settings_changed", "user", `{"require_tailnet":true}`) // not the store's
		}, "nocheck"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := openTestDB(t)
			if err := Migrate(db, Migrations()); err != nil {
				t.Fatal(err)
			}
			if err := Rollback(db, Migrations(), 37); err != nil {
				t.Fatal(err)
			}
			c.setup(t, db)
			if err := Migrate(db, Migrations()); err != nil {
				t.Fatal(err)
			}
			s, err := NewLog(db).MeshSettings(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			switch c.want {
			case "":
				if len(s.Networks) != 0 {
					t.Fatalf("fresh install chose %+v", s)
				}
			default:
				ch, ok := s.Choice("tailscale")
				if !ok || !ch.Listen || ch.TailnetCheck != (c.want == "check") || ch.DecidedAt.IsZero() || len(s.Networks) != 1 {
					t.Fatalf("got %+v", s)
				}
				if s.RequireTailnet == nil || *s.RequireTailnet != (c.want == "check") {
					t.Fatalf("require_tailnet %+v", s.RequireTailnet)
				}
			}
		})
	}
}

func insertEvent(t *testing.T, db *sql.DB, typ, actor, payload string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO events (timestamp, type, actor, payload) VALUES (?, ?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339Nano), typ, actor, payload); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverable(t *testing.T) {
	_, l := migratedLog(t)
	ctx := t.Context()
	if d, err := l.Discoverable(ctx); err != nil || d.Open(time.Now()) {
		t.Fatalf("%+v %v", d, err)
	}
	// Asked for an hour: capped at ten minutes.
	d, err := l.SetDiscoverable(ctx, time.Now().Add(time.Hour), "user", false)
	if err != nil || d.Until.After(time.Now().Add(contracts.DiscoverableMax+time.Second)) || !d.Open(time.Now()) {
		t.Fatalf("%+v %v", d, err)
	}
	if got, _ := l.Discoverable(ctx); !got.Open(time.Now()) {
		t.Fatal("not read back")
	}
	if _, err := l.SetDiscoverable(ctx, time.Time{}, "user", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := l.Discoverable(ctx); got.Open(time.Now()) {
		t.Fatal("still open after closing")
	}
}

// Round-1 review S5: renewals keep a window open at most 30 minutes from
// when it was first opened; a renewal of a closed window is refused (it
// must be opened again), and a fresh open starts a new 30 minutes.
func TestDiscoverableRenewalCap(t *testing.T) {
	db, l := migratedLog(t)
	ctx := t.Context()
	if _, err := l.SetDiscoverable(ctx, time.Now().Add(5*time.Minute), "user", true); !errors.Is(err, ErrWindowClosed) {
		t.Fatalf("renew a closed window: %v", err)
	}
	// Opened 25 minutes ago: a renewal stops at 30.
	opened := time.Now().UTC().Add(-25 * time.Minute)
	insertEvent(t, db, "device.discoverable", MeshSettingsActor, `{"until":"`+time.Now().UTC().Add(2*time.Minute).Format(time.RFC3339Nano)+`","opened_at":"`+opened.Format(time.RFC3339Nano)+`"}`)
	d, err := l.SetDiscoverable(ctx, time.Now().Add(10*time.Minute), "user", true)
	if err != nil || d.Until.After(opened.Add(contracts.DiscoverableAbsoluteMax).Add(time.Second)) || !d.OpenedAt.Equal(opened) {
		t.Fatalf("renewed %+v %v", d, err)
	}
	// Opened 31 minutes ago (somehow still open): no renewal.
	old := time.Now().UTC().Add(-31 * time.Minute)
	insertEvent(t, db, "device.discoverable", MeshSettingsActor, `{"until":"`+time.Now().UTC().Add(2*time.Minute).Format(time.RFC3339Nano)+`","opened_at":"`+old.Format(time.RFC3339Nano)+`"}`)
	if _, err := l.SetDiscoverable(ctx, time.Now().Add(5*time.Minute), "user", true); !errors.Is(err, ErrWindowClosed) {
		t.Fatalf("renew past 30 minutes: %v", err)
	}
	d, err = l.SetDiscoverable(ctx, time.Now().Add(5*time.Minute), "user", false)
	if err != nil || !d.Open(time.Now()) || time.Since(d.OpenedAt) > time.Minute {
		t.Fatalf("reopen %+v %v", d, err)
	}
	// Closing records when.
	d, _ = l.SetDiscoverable(ctx, time.Time{}, "user", false)
	if d.ClosedAt == nil || d.Open(time.Now()) {
		t.Fatalf("closed %+v", d)
	}
}
