package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func TestDevicePrintsTheFingerprintNeverTheKey(t *testing.T) {
	home := t.TempDir() + "/mads"
	t.Setenv("MADS_HOME", home)
	var out bytes.Buffer
	if err := cmdDevice(nil, &out); err != nil {
		t.Fatal(err)
	}
	id, err := devicekey.Load(home)
	if err != nil {
		t.Fatalf("mads device didn't create the key: %v", err)
	}
	fp := id.Fingerprint()
	if !strings.Contains(out.String(), fp.Grouped()) || !strings.Contains(out.String(), "mads pair --accept "+fp.Grouped()) {
		t.Fatalf("output:\n%s", out.String())
	}
	// Idempotent: a second run shows the same device.
	var again bytes.Buffer
	if err := cmdDevice(nil, &again); err != nil || again.String() != out.String() {
		t.Fatalf("second run: %v\n%s", err, again.String())
	}

	var js bytes.Buffer
	if err := cmdDevice([]string{"--json"}, &js); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(js.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["fingerprint"] != string(fp) || got["short"] != fp.Short() || got["name"] == "" {
		t.Fatalf("--json = %v", got)
	}
	pub, err := devicekey.DecodePublicKey(got["public_key"])
	if err != nil || devicekey.FingerprintOf(pub) != fp {
		t.Fatalf("--json public key doesn't match the fingerprint: %v", err)
	}
	// Nothing printed is the private key (the DER tail is the seed).
	raw, _ := os.ReadFile(devicekey.Path(home))
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatal("no PEM in the key file")
	}
	secret := base64.StdEncoding.EncodeToString(block.Bytes)
	for _, printed := range []string{out.String(), js.String()} {
		if strings.Contains(printed, secret[len(secret)-40:]) || strings.Contains(printed, "PRIVATE KEY") {
			t.Fatalf("mads device printed the private key:\n%s", printed)
		}
	}

	if err := cmdDevice([]string{"extra"}, io.Discard); err == nil {
		t.Fatal("an extra argument was accepted")
	}
}

func TestDeviceRefusesALooseKeyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MADS_HOME", home)
	if err := cmdDevice(nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(devicekey.Path(home), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cmdDevice(nil, &out); err == nil || !strings.Contains(err.Error(), "other users") || out.Len() != 0 {
		t.Fatalf("err %v, stdout %q", err, out.String())
	}
}

func cliFingerprint(seed byte) devicekey.Fingerprint {
	return devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{seed}, ed25519.PublicKeySize)))
}

// pairHarness is a real Data Service behind its door, with $MADS_HOME
// holding its token, as `mads pair` on the host would find it.
func pairHarness(t *testing.T) (*dataclient.Client, func(args ...string) (string, error)) {
	t.Helper()
	g := datatest.StartGuarded(t)
	t.Setenv("MADS_HOME", g.Home)
	data := dataclient.New(g.URL, "test", dataclient.WithToken(g.Token))
	return data, func(args ...string) (string, error) {
		var out bytes.Buffer
		err := cmdPair(append(args, "-data", g.URL), &out)
		return out.String(), err
	}
}

func TestPairAcceptListRevoke(t *testing.T) {
	data, pair := pairHarness(t)
	ctx := context.Background()
	phone := cliFingerprint(1)

	// Any printed form is accepted; a new device defaults to view and
	// isn't viewed from here.
	out, err := pair("--accept", strings.ToLower(phone.Grouped()), "--name", "phone")
	if err != nil || !strings.Contains(out, "paired phone ("+phone.Short()+"): view") {
		t.Fatalf("accept: %v %q", err, out)
	}
	p, err := data.Peer(ctx, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if p.Fingerprint != string(phone) || p.Level() != contracts.GrantView || p.IsViewed() || p.Pairing == nil || p.Pairing.How != contracts.PairAccept {
		t.Fatalf("stored %+v", p)
	}

	// Accepting it again updates it in place: raise to chat, rename.
	if out, err := pair("--accept", string(phone), "--grant", "chat", "--name", "pixel"); err != nil || !strings.Contains(out, "pixel") {
		t.Fatalf("re-accept: %v %q", err, out)
	}
	list, _ := data.Peers(ctx)
	if len(list) != 1 || list[0].Name != "pixel" || list[0].Level() != contracts.GrantChat {
		t.Fatalf("after re-accept: %+v", list)
	}

	// A device already viewed (added from the app) gains its paired side
	// when accepted under its name; another key can't take that name.
	if err := data.CreatePeer(ctx, &contracts.Peer{Name: "pop-os", MeshURL: "http://pop-os:17380"}); err != nil {
		t.Fatal(err)
	}
	popos := cliFingerprint(2)
	if _, err := pair("--accept", string(popos), "--name", "pop-os", "--grant", "control"); err != nil {
		t.Fatal(err)
	}
	if _, err := pair("--accept", string(cliFingerprint(3)), "--name", "pop-os"); err == nil || !strings.Contains(err.Error(), "already paired") {
		t.Fatalf("a second key under a paired name: %v", err)
	}
	// Without a name, a device gets one from its fingerprint.
	anon := cliFingerprint(4)
	if out, err := pair("--accept", string(anon), "--grant", "none"); err != nil || !strings.Contains(out, "device-"+strings.ToLower(string(anon[:8]))+" ") || !strings.Contains(out, ": none") {
		t.Fatalf("unnamed accept: %v %q", err, out)
	}

	out, err = pair("--list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NAME", "pixel", phone.Short(), "chat", "pop-os", popos.Short(), "control", "yes", "accept"} {
		if !strings.Contains(out, want) {
			t.Errorf("--list lacks %q:\n%s", want, out)
		}
	}
	var listed []contracts.Peer
	out, err = pair("--list", "--json")
	if err != nil || json.Unmarshal([]byte(out), &listed) != nil || len(listed) != 3 {
		t.Fatalf("--list --json: %v %q", err, out)
	}

	// Revoking a viewed device keeps it viewed; a paired-only one goes.
	if out, err := pair("--revoke", "pop-os"); err != nil || !strings.Contains(out, "unpaired pop-os") {
		t.Fatalf("revoke pop-os: %v %q", err, out)
	}
	if p, err := data.Peer(ctx, "pop-os"); err != nil || p.IsPaired() || !p.IsViewed() || p.Grant != nil {
		t.Fatalf("pop-os after revoke: %+v, %v", p, err)
	}
	if _, err := pair("--revoke", phone.Grouped()); err != nil {
		t.Fatal(err)
	}
	if _, err := data.Peer(ctx, "pixel"); err == nil {
		t.Fatal("a revoked paired-only device is still listed")
	}
	if _, err := pair("--revoke", "pop-os"); err == nil {
		t.Fatal("revoking an unpaired device succeeded")
	}

	// The audit trail (D8).
	evs, _ := data.Events(ctx, 0, "", []string{"device.*"})
	var types []string
	for _, e := range evs {
		types = append(types, string(e.Type))
	}
	if got := strings.Join(types, " "); got != "device.paired device.grant_changed device.paired device.paired device.unpaired device.unpaired" {
		t.Fatalf("device events: %s", got)
	}
}

// Q2: `mads pair` on the host, with the install token, is how admin is
// granted.
func TestPairGrantsAdminOnTheHost(t *testing.T) {
	data, pair := pairHarness(t)
	fp := cliFingerprint(5)
	if _, err := pair("--accept", string(fp), "--name", "laptop", "--grant", "admin"); err != nil {
		t.Fatal(err)
	}
	if p, _ := data.Peer(context.Background(), "laptop"); p.Level() != contracts.GrantAdmin {
		t.Fatalf("level %q, want admin", p.Level())
	}
}

func TestPairRefusesBadInput(t *testing.T) {
	_, pair := pairHarness(t)
	fp := string(cliFingerprint(6))
	for name, args := range map[string][]string{
		"no mode":           {},
		"two modes":         {"--list", "--revoke", "x"},
		"short fingerprint": {"--accept", cliFingerprint(6).Short()},
		"not a fingerprint": {"--accept", "hello"},
		"unknown grant":     {"--accept", fp, "--grant", "root"},
		"mesh-url alone":    {"--accept", fp, "--mesh-url", "http://x:1"},
		"view without url":  {"--accept", fp, "--view"},
		"bad name":          {"--accept", fp, "--name", "has space"},
		"stray argument":    {"--list", "extra"},
		"reserved name":     {"--accept", fp, "--name", "candidates"},
		"id-shaped name":    {"--accept", fp, "--name", "per_abc"},
	} {
		if out, err := pair(args...); err == nil {
			t.Errorf("%s: accepted (%q)", name, out)
		}
	}
	// --view with a mesh URL makes a device both paired and viewed.
	if out, err := pair("--accept", fp, "--name", "box", "--view", "--mesh-url", "http://box:17380/"); err != nil || !strings.Contains(out, "viewed") {
		t.Fatalf("--view --mesh-url: %v %q", err, out)
	}
}

// This host's own fingerprint is never pinned as a remote device.
func TestPairRefusesThisDevicesOwnKey(t *testing.T) {
	_, pair := pairHarness(t)
	var dev bytes.Buffer
	if err := cmdDevice([]string{"--json"}, &dev); err != nil {
		t.Fatal(err)
	}
	var self map[string]string
	if err := json.Unmarshal(dev.Bytes(), &self); err != nil {
		t.Fatal(err)
	}
	if _, err := pair("--accept", self["fingerprint"]); err == nil || !strings.Contains(err.Error(), "own fingerprint") {
		t.Fatalf("accepting this device's own key: %v", err)
	}
}

// A device key that can't be read fails the accept: the own-key check
// isn't skipped silently.
func TestPairFailsWhenThisDevicesKeyIsUnreadable(t *testing.T) {
	_, pair := pairHarness(t)
	if err := cmdDevice(nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(devicekey.Path(os.Getenv("MADS_HOME")), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := pair("--accept", string(cliFingerprint(20))); err == nil || !strings.Contains(err.Error(), "own key") || !strings.Contains(err.Error(), "other users") {
		t.Fatalf("accept with an unreadable device key: %v", err)
	}
}
