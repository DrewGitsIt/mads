package localauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Adversarial round 2 (docs/LOCAL-FRONT-DOOR-DESIGN.md): the door's
// credential channels.

// TestRound2CrossKindCredentialsAreRefused pins the separation: no
// credential works outside the channel it was made for — the bearer (H7).
func TestRound2CrossKindCredentialsAreRefused(t *testing.T) {
	f := newDoorFixture(t)
	scoped, err := f.reg.Mint("ses_a")
	if err != nil {
		t.Fatal(err)
	}
	for name, set := range map[string]func(*http.Request){
		"install token as a cookie":         cookie(f.store.Token()),
		"scoped token as a cookie":          cookie(scoped),
		"install token with a bogus scheme": func(r *http.Request) { r.Header.Set("Authorization", "Token "+f.store.Token()) },
		"scoped token with a bogus scheme":  func(r *http.Request) { r.Header.Set("Authorization", "Token "+scoped) },
	} {
		if w := f.do(t, "GET", "/sessions", set); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, w.Code)
		}
	}
}

// TestRound2DoorWatchersDontLeak: every authenticated request starts a
// watcher goroutine; none may outlive its request, whether the request
// finished, the client hung up mid-stream, or a rotation ended it.
func TestRound2DoorWatchersDontLeak(t *testing.T) {
	home := t.TempDir()
	store, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /quick", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /events", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	door := NewDoor(DoorOptions{
		Name: "test", Store: store,
		Classify:   func(r *http.Request) string { _, p := mux.Handler(r); return p },
		WatchEvery: 5 * time.Millisecond,
	})
	srv := httptest.NewServer(door.Wrap(mux))
	defer srv.Close()
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	settle := func() int {
		var n int
		for i := 0; i < 100; i++ {
			runtime.GC()
			time.Sleep(20 * time.Millisecond)
			n = runtime.NumGoroutine()
		}
		return n
	}
	base := settle()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "/quick"
			if i%2 == 0 {
				path = "/events"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+path, nil)
			req.Header.Set("Authorization", "Bearer "+store.Token())
			if resp, err := client.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}(i)
	}
	wg.Wait()
	// Some streams still open when the token rotates.
	streams := make([]*http.Response, 0, 10)
	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest("GET", srv.URL+"/events", nil)
		req.Header.Set("Authorization", "Bearer "+store.Token())
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		streams = append(streams, resp)
	}
	if _, err := Rotate(home); err != nil {
		t.Fatal(err)
	}
	store.Token()
	for _, r := range streams {
		_ = r.Body.Close()
	}
	client.CloseIdleConnections()
	if after := settle(); after > base+3 {
		t.Fatalf("goroutines: %d before, %d after every request ended: watchers leak", base, after)
	}
	door.mu.Lock()
	n := len(door.active)
	door.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d requests still registered as active", n)
	}
}

// TestRotationSparesRequestsMadeWithTheNewToken (fixed in round 2): a
// rotation is noticed
// lazily, by whichever Token() call first sees the new file; its hooks (end
// every open request) run after that call has already published the new
// token. A request carrying the *new* token that authenticates in that
// window is registered as active and then cancelled by the rotation it
// arrived after. Now the rotation hook ends only requests whose credential
// it killed: only requests authenticated before the rotation end.
func TestRotationSparesRequestsMadeWithTheNewToken(t *testing.T) {
	home := t.TempDir()
	store, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	// The real window is the Router's own hooks (three RevokeAlls and a
	// log line, microseconds): a slow hook widens it so the race is
	// deterministic. Without the sleep it reproduces only rarely; the logic
	// flaw is the same. Low impact: a stream the UI reconnects.
	store.OnRotate(func() { time.Sleep(30 * time.Millisecond) })
	var cancelled atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /work", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			cancelled.Add(1)
		case <-time.After(150 * time.Millisecond):
		}
	})
	door := NewDoor(DoorOptions{
		Name: "test", Store: store,
		Classify: func(r *http.Request) string { _, p := mux.Handler(r); return p },
	})
	h := door.Wrap(mux)
	newTok, err := Rotate(home)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Duration(i%10) * time.Millisecond)
			r := httptest.NewRequest("GET", "/work", nil)
			r.Header.Set("Authorization", "Bearer "+newTok)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
		}(i)
	}
	wg.Wait()
	if n := cancelled.Load(); n > 0 {
		t.Fatalf("%d of 40 requests made with the new token after the rotation were cancelled by it", n)
	}
}

// TestRound2ExistingAndSymlinkedHomeIsMadePrivate: an existing 0755
// $MADS_HOME full of state is tightened to 0700 without touching what's in
// it, and a $MADS_HOME that is a symlink has its *target* tightened (the
// link's own mode means nothing).
func TestRound2ExistingAndSymlinkedHomeIsMadePrivate(t *testing.T) {
	real := filepath.Join(t.TempDir(), "real-home")
	if err := os.MkdirAll(filepath.Join(real, "mcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(real, 0o755); err != nil {
		t.Fatal(err)
	}
	creds := filepath.Join(real, "credentials.json")
	if err := os.WriteFile(creds, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "mads-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	s, err := LoadOrCreate(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(real); fi.Mode().Perm() != 0o700 {
		t.Fatalf("symlinked $MADS_HOME's target is %04o, want 0700", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Join(real, "mcp")); fi.Mode().Perm() != 0o755 {
		t.Fatalf("a subdirectory's mode was changed: %04o", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(creds); string(b) != "{}" { //nolint:gosec // test file
		t.Fatal("existing state was touched")
	}
	if tok, err := ReadToken(real); err != nil || tok != s.Token() {
		t.Fatalf("the token isn't in the link's target: %v", err)
	}
	// A dangling symlink: MkdirAll can't create through it.
	dangling := filepath.Join(t.TempDir(), "dangling")
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone", "x"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dangling); err == nil {
		if fi, err := os.Stat(dangling); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("dangling-symlink home: %v %v", fi, err)
		}
	}
}
