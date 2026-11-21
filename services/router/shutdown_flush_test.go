package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// What a device lets this one do, heard but held back by
// the write debounce, is written when the real Router stops: main waits
// for the mesh manager's shutdown flush before exiting.
func TestShutdownWritesTheHeldBackReverseLevel(t *testing.T) {
	homeDir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dataURL := datatest.Start(t)
	data := dataclient.New(dataURL, "boottest")

	// A host this Router has paired (granting it view), whose hello first
	// grants control, then admin.
	peerID, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	var admin atomic.Bool
	var hellos atomic.Int32
	peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		access := []string{"view", "chat", "control"}
		if admin.Load() {
			access = append(access, "admin")
		}
		hellos.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"node": "pop-os", "mesh_api": 1, "access": access, "device": string(peerID.Fingerprint()), "hosts_sessions": true})
	}))
	cfg, err := devicekey.ServerConfig(peerID)
	if err != nil {
		t.Fatal(err)
	}
	peer.TLS = cfg
	peer.StartTLS()
	t.Cleanup(peer.Close)
	no := false
	if err := data.CreatePeer(context.Background(), &contracts.Peer{Name: "pop-os", MeshURL: "http://" + peer.Listener.Addr().String(), Viewed: &no,
		Fingerprint: string(peerID.Fingerprint()), Grant: &contracts.PeerGrant{Level: contracts.GrantView}}); err != nil {
		t.Fatal(err)
	}
	level := func() contracts.GrantLevel {
		row, err := data.Peer(context.Background(), "pop-os")
		if err != nil || row.Remote == nil {
			return "unset"
		}
		return row.Remote.Level
	}

	router := spawnListening(t, "router", []string{
		bootRouterBin, "-addr", "127.0.0.1:0", "-data-url", dataURL,
		"-session-cmd", bootTestchildBin, "-pipeline-cmd", bootTestchildBin, "-mads-home", homeDir,
	}, bootCleanEnv(homeDir))
	defer router.terminate(t, 10*time.Second)
	bootEventually(t, 20*time.Second, func() bool { return level() == contracts.GrantControl })
	admin.Store(true)
	seen := hellos.Load()
	bootEventually(t, 20*time.Second, func() bool { return hellos.Load() > seen })
	time.Sleep(500 * time.Millisecond) // the probe's answer noted, held back
	if got := level(); got != contracts.GrantControl {
		t.Fatalf("a raise within the minute was written at once: %s", got)
	}
	router.terminate(t, 10*time.Second)
	if got := level(); got != contracts.GrantAdmin {
		t.Fatalf("after the Router stopped: %s, want admin (the held-back answer)", got)
	}
}
