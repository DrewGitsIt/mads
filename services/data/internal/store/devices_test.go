package store

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
)

func testFP(seed byte) devicekey.Fingerprint {
	return devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{seed}, ed25519.PublicKeySize)))
}

// The device activity log is device.* plus whatever a device did, newest
// first, filterable by device (by fingerprint in device.* payloads, by
// short form in the actor, which survives a rename); each device's last
// use is its actor's newest event.
func TestDeviceActivity(t *testing.T) {
	l := openMigratedDB(t)
	ctx := context.Background()
	phone, mac := testFP(1), testFP(2)
	appendEv := func(typ contracts.EventType, actor string, payload any) int64 {
		raw, _ := json.Marshal(payload)
		env := contracts.Envelope{Type: typ, Actor: actor, Payload: raw, SessionID: "ses_1"}
		if err := l.Append(ctx, &env); err != nil {
			t.Fatal(err)
		}
		return env.Seq
	}
	paired := appendEv(contracts.DevicePaired, "user", contracts.DeviceChange{Fingerprint: string(phone), Name: "phone"})
	appendEv(contracts.SessionCreated, "user", nil) // not a device's
	sent := appendEv(contracts.SessionDeviceMessage, "device:phone/"+phone.Short(), contracts.DeviceMessage{})
	refused := appendEv(contracts.DeviceRefused, "mesh", contracts.DeviceRefusal{Fingerprint: string(mac), Code: "no_access"})
	renamed := appendEv(contracts.SessionRenamed, "device:iphone/"+phone.Short(), nil)
	macSent := appendEv(contracts.SessionDeviceMessage, "device:mac/"+mac.Short(), contracts.DeviceMessage{})

	seqs := func(evs []contracts.Envelope) []int64 {
		var out []int64
		for _, e := range evs {
			out = append(out, e.Seq)
		}
		return out
	}
	all, err := l.DeviceActivity(ctx, DeviceActivityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := seqs(all), []int64{macSent, renamed, refused, sent, paired}; !equalSeqs(got, want) {
		t.Fatalf("all = %v, want %v", got, want)
	}
	ph, _ := l.DeviceActivity(ctx, DeviceActivityFilter{Fingerprint: string(phone), Short: phone.Short()})
	if got, want := seqs(ph), []int64{renamed, sent, paired}; !equalSeqs(got, want) {
		t.Fatalf("phone = %v, want %v", got, want)
	}
	page, _ := l.DeviceActivity(ctx, DeviceActivityFilter{Before: refused, Limit: 1})
	if got, want := seqs(page), []int64{sent}; !equalSeqs(got, want) {
		t.Fatalf("page = %v, want %v", got, want)
	}

	last, err := l.DeviceLastUsed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	bySeq := map[string]int64{}
	for _, u := range last {
		bySeq[u.Actor] = u.Seq
	}
	if bySeq["device:phone/"+phone.Short()] != sent || bySeq["device:iphone/"+phone.Short()] != renamed || bySeq["device:mac/"+mac.Short()] != macSent || len(bySeq) != 3 {
		t.Fatalf("last used = %v", bySeq)
	}
}

// The queries use indexes (migration 0036, and idx_events_type), so a
// large log of other events isn't scanned, and last use isn't sorted.
func TestDeviceActivityUsesTheIndex(t *testing.T) {
	l := openMigratedDB(t)
	plan := func(q string) string {
		t.Helper()
		rows, err := l.db.Query("EXPLAIN QUERY PLAN " + q)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		var b strings.Builder
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			b.WriteString(detail + "\n")
		}
		return b.String()
	}
	if p := plan(`SELECT seq FROM events WHERE ` + deviceActorTerm + ` AND seq < 10 ORDER BY seq DESC LIMIT 5`); !strings.Contains(p, "idx_events_device_actor") {
		t.Fatalf("activity by actor doesn't use the device actor index:\n%s", p)
	}
	if p := plan(`SELECT seq FROM events WHERE ` + deviceTypeTerm + ` AND seq < 10 ORDER BY seq DESC LIMIT 5`); !strings.Contains(p, "idx_events_type") {
		t.Fatalf("device.* doesn't use the type index:\n%s", p)
	}
	if p := plan(lastUsedQuery); !strings.Contains(p, "idx_events_device_actor_last") || strings.Contains(p, "TEMP B-TREE") {
		t.Fatalf("last used scans or sorts:\n%s", p)
	}
}

func equalSeqs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A database that ran an earlier 0036 (which also made the last-use
// index) still migrates: 0037 makes it only if missing.
func TestLastUseIndexMigratesOverAnEarlier0036(t *testing.T) {
	db := openTestDB(t)
	if err := Migrate(db, Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := Rollback(db, Migrations(), 36); err != nil {
		t.Fatal(err)
	}
	// What the earlier 0036 left behind.
	if _, err := db.Exec(`CREATE INDEX idx_events_device_actor_last ON events (actor, seq) WHERE substr(actor, 1, 7) = 'device:'`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, Migrations()); err != nil {
		t.Fatalf("0037 over an earlier 0036: %v", err)
	}
	if v, _ := Version(db); v != 37 {
		t.Fatalf("version %d", v)
	}
}
