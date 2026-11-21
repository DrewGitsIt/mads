package paired

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/localauth"
)

// This host's mesh settings ride along with the directory: read with the
// first load, and again whenever device.settings_changed says they
// changed, so "Only accept devices on my tailnet" applies without a
// restart. A reload that finds them unchanged doesn't call the hook.
func TestDirectoryFollowsMeshSettings(t *testing.T) {
	f := newFlakyData(t)
	w := f.writer()
	ctx := context.Background()

	d := New(dataclient.New(f.url, "mesh", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile))))
	d.minBackoff, d.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	var mu sync.Mutex
	var seen []contracts.MeshSettings
	d.OnSettings(func(s contracts.MeshSettings) {
		mu.Lock()
		seen = append(seen, s)
		mu.Unlock()
	})
	got := func() []contracts.MeshSettings {
		mu.Lock()
		defer mu.Unlock()
		return append([]contracts.MeshSettings(nil), seen...)
	}
	run(t, d)
	eventually(t, "the first load's settings", func() bool { return len(got()) == 1 })
	if s := got()[0]; s.RequireTailnet != nil {
		t.Fatalf("never set: %+v", s)
	}

	off := false
	if _, err := w.SetMeshSettings(ctx, contracts.MeshSettings{RequireTailnet: &off}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the change", func() bool { return len(got()) == 2 })
	if s := got()[1]; s.RequireTailnet == nil || *s.RequireTailnet {
		t.Fatalf("after turning it off: %+v", s)
	}

	// A peer change reloads the directory, but the settings didn't change.
	no := false
	if err := w.CreatePeer(ctx, &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: testFingerprint(3)}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the new pairing", func() bool { _, ok := d.Lookup(testFingerprint(3)); return ok })
	if n := len(got()); n != 2 {
		t.Fatalf("settings hook ran %d times, want 2 (unchanged settings don't call it)", n)
	}
}

// A settings read that fails never holds the paired list back: a
// revocation applies, and the last good settings stay (A).
func TestSettingsFailureNeverBlocksARevocation(t *testing.T) {
	f := newFlakyData(t)
	w := f.writer()
	ctx := context.Background()
	no, off := false, false
	phone := &contracts.Peer{Name: "phone", Viewed: &no, Fingerprint: testFingerprint(4), Grant: &contracts.PeerGrant{Level: contracts.GrantChat}}
	if err := w.CreatePeer(ctx, phone); err != nil {
		t.Fatal(err)
	}
	if _, err := w.SetMeshSettings(ctx, contracts.MeshSettings{RequireTailnet: &off}); err != nil {
		t.Fatal(err)
	}
	// The settings route breaks (a Data Service in trouble); everything
	// else answers.
	target, _ := url.Parse(f.real)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	var brokenSettings atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if brokenSettings.Load() && r.URL.Path == "/devices/settings" {
			http.Error(rw, "boom", http.StatusInternalServerError)
			return
		}
		proxy.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)

	d := New(dataclient.New(srv.URL, "mesh", dataclient.WithTokenSource(localauth.FileSource(f.tokenFile))))
	d.minBackoff, d.maxBackoff = 10*time.Millisecond, 50*time.Millisecond
	var mu sync.Mutex
	var last *contracts.MeshSettings
	d.OnSettings(func(s contracts.MeshSettings) {
		mu.Lock()
		last = &s
		mu.Unlock()
	})
	run(t, d)
	eventually(t, "the first load", func() bool { _, ok := d.Lookup(phone.Fingerprint); return ok })
	eventually(t, "the settings", func() bool { mu.Lock(); defer mu.Unlock(); return last != nil })

	brokenSettings.Store(true)
	if err := w.DeletePeer(ctx, phone.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the revocation, with settings unreadable", func() bool { _, ok := d.Lookup(phone.Fingerprint); return !ok })
	mu.Lock()
	defer mu.Unlock()
	if last.RequireTailnet == nil || *last.RequireTailnet {
		t.Fatalf("settings after a failed read: %+v, want the last good (off)", last)
	}
}
