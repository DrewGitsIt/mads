package tailnet

import (
	"context"
	"errors"
	"testing"

	"github.com/drewkidwell/mads/pkg/meshnet/tailscale"
	"github.com/drewkidwell/mads/pkg/meshnet/tailscale/tailscaletest"
)

// The shim reads a socket as the Mesh Service always has; the LocalAPI
// client's own tests (pkg/meshnet/tailscale) cover macOS.
func TestShim(t *testing.T) {
	f, sock := tailscaletest.StartSocket(t)
	f.Set(func(f *tailscaletest.Fake) {
		f.Status = tailscale.Status{BackendState: "Running", Self: tailscaletest.Node("pop-os", 42, "100.64.0.70")}
		f.WhoIs["100.70.1.1"] = tailscaletest.WhoIsAnswer{Node: "mac.tail4c2e.ts.net", UserID: 42}
	})
	c := New(sock)
	self, err := c.Self(context.Background())
	if err != nil || self.IPv4 != "100.64.0.70" {
		t.Fatalf("%+v %v", self, err)
	}
	if _, err := c.WhoIs(context.Background(), "100.71.0.1:1"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if who, err := Locate(sock).WhoIs(context.Background(), "100.70.1.1:1"); err != nil || who.UserID != 42 {
		t.Fatalf("%+v %v", who, err)
	}
}
