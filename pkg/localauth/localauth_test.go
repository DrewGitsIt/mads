package localauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLoadOrCreateWritesAPrivateToken(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mads")
	s, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	tok := s.Token()
	if len(tok) != 43 {
		t.Fatalf("token %q: want 43 base64url chars", tok)
	}
	fi, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token mode %04o, want 0600", perm)
	}
	di, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("new $MADS_HOME mode %04o, want 0700", perm)
	}
	again, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	if again.Token() != tok {
		t.Fatal("a second LoadOrCreate minted a different token")
	}
	if got, err := ReadToken(home); err != nil || got != tok {
		t.Fatalf("ReadToken = %q, %v", got, err)
	}
}

func TestLoadWithoutATokenIsErrNoToken(t *testing.T) {
	home := t.TempDir()
	if _, err := Load(home); !errors.Is(err, ErrNoToken) {
		t.Fatalf("Load = %v, want ErrNoToken", err)
	}
	if tok, err := ClientToken(home); err != nil || tok != "" {
		t.Fatalf("ClientToken = %q, %v; want empty, nil", tok, err)
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Fatal("Load or ClientToken created a token")
	}
}

func TestLoadRefusesAReadableToken(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o660, 0o644, 0o602} {
		home := t.TempDir()
		if _, err := LoadOrCreate(home); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(Path(home), mode); err != nil {
			t.Fatal(err)
		}
		for name, load := range map[string]func() error{
			"Load":         func() error { _, err := Load(home); return err },
			"LoadOrCreate": func() error { _, err := LoadOrCreate(home); return err },
			"ReadToken":    func() error { _, err := ReadToken(home); return err },
			"ClientToken":  func() error { _, err := ClientToken(home); return err },
		} {
			err := load()
			if err == nil {
				t.Fatalf("%s accepted a mode %04o token", name, mode)
			}
			if !strings.Contains(err.Error(), "chmod 600") || !strings.Contains(err.Error(), "mads token rotate") {
				t.Fatalf("%s error isn't actionable: %v", name, err)
			}
		}
	}
}

// fakeInfo lets the owner check see a file another user owns, which a
// non-root test can't create.
type fakeInfo struct {
	os.FileInfo
	sys any
}

func (f fakeInfo) Sys() any { return f.sys }

func TestCheckFileRefusesAnotherOwner(t *testing.T) {
	home := t.TempDir()
	if _, err := LoadOrCreate(home); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	other := foreignOwned(t, fi)
	err = checkFile(Path(home), fakeInfo{fi, other})
	if err == nil || !strings.Contains(err.Error(), "not you") {
		t.Fatalf("checkFile = %v, want an owner refusal", err)
	}
	if err := checkFile(Path(home), fi); err != nil {
		t.Fatalf("own file refused: %v", err)
	}
}

func TestLoadRefusesANonRegularFile(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(Path(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("a directory was accepted as the token")
	}
}

func TestLoadRefusesATruncatedToken(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(Path(home), []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil || !strings.Contains(err.Error(), "mads token rotate") {
		t.Fatalf("Load = %v, want a refusal naming the fix", err)
	}
}

func TestConcurrentFirstStartsAgreeOnOneToken(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mads")
	const n = 16
	toks := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := LoadOrCreate(home)
			if err != nil {
				t.Error(err)
				return
			}
			toks[i] = s.Token()
		}()
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if toks[i] != toks[0] {
			t.Fatalf("goroutine %d got a different token", i)
		}
	}
	assertOnlyTokenFiles(t, home)
}

// TestConcurrentFirstStartsAcrossProcesses is the real case: the Data
// Service and Router starting at once, each its own process.
func TestConcurrentFirstStartsAcrossProcesses(t *testing.T) {
	if os.Getenv("LOCALAUTH_HELPER_HOME") != "" {
		t.Skip("helper")
	}
	home := filepath.Join(t.TempDir(), "mads")
	const n = 8
	cmds := make([]*exec.Cmd, n)
	outs := make([]*bytes.Buffer, n)
	for i := range n {
		cmds[i] = exec.Command(os.Args[0], "-test.run=^TestHelperLoadOrCreate$") //nolint:gosec // the test binary itself
		cmds[i].Env = append(os.Environ(), "LOCALAUTH_HELPER_HOME="+home)
		outs[i] = &bytes.Buffer{}
		cmds[i].Stdout = outs[i]
		if err := cmds[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("helper %d: %v\n%s", i, err, outs[i])
		}
		tok := ""
		for _, line := range strings.Split(outs[i].String(), "\n") {
			if v, ok := strings.CutPrefix(line, "TOKEN="); ok {
				tok = v
			}
		}
		if tok == "" {
			t.Fatalf("helper %d printed no token:\n%s", i, outs[i])
		}
		if first == "" {
			first = tok
		} else if tok != first {
			t.Fatalf("helper %d got a different token", i)
		}
	}
	if got, err := ReadToken(home); err != nil || got != first {
		t.Fatalf("file holds %q (%v), processes agreed on %q", got, err, first)
	}
	assertOnlyTokenFiles(t, home)
}

func TestHelperLoadOrCreate(t *testing.T) {
	home := os.Getenv("LOCALAUTH_HELPER_HOME")
	if home == "" {
		t.Skip("only run as a helper process")
	}
	s, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("TOKEN=%s\n", s.Token())
}

func assertOnlyTokenFiles(t *testing.T, home string) {
	t.Helper()
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != FileName && e.Name() != FileName+".lock" {
			t.Errorf("left behind %s", e.Name())
		}
	}
}

func TestRotationIsPickedUpAndFiresHooks(t *testing.T) {
	home := t.TempDir()
	s, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	old := s.Token()
	fired := 0
	s.OnRotate(func() { fired++ })
	if !s.Check(old) {
		t.Fatal("current token refused")
	}
	fresh, err := Rotate(home)
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old {
		t.Fatal("Rotate wrote the same token")
	}
	if s.Check(old) {
		t.Fatal("the old token still passes after rotation")
	}
	if !s.Check(fresh) {
		t.Fatal("the rotated token is refused")
	}
	if fired != 1 {
		t.Fatalf("OnRotate fired %d times, want 1", fired)
	}
	if fi, _ := os.Stat(Path(home)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("rotated token mode %04o", fi.Mode().Perm())
	}
	assertOnlyTokenFiles(t, home)
}

func TestStoreKeepsTheLastGoodTokenWhenTheFileTurnsUnsafe(t *testing.T) {
	home := t.TempDir()
	s, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	tok := s.Token()
	if err := os.Chmod(Path(home), 0o644); err != nil {
		t.Fatal(err)
	}
	// Touch so the mtime check notices.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(Path(home), later, later); err != nil {
		t.Fatal(err)
	}
	if got := s.Token(); got != tok {
		t.Fatalf("Token = %q after chmod, want the last good one", got)
	}
}

// TestFileSourceFollowsRotation: a child reads the token from the path it
// was given and follows a rotation (H4); before the file exists it yields
// "" and keeps trying.
func TestFileSourceFollowsRotation(t *testing.T) {
	home := t.TempDir()
	src := FileSource(Path(home))
	if got := src(); got != "" {
		t.Fatalf("FileSource with no file = %q, want empty", got)
	}
	s, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := src(); got != s.Token() {
		t.Fatal("FileSource didn't pick the file up once it appeared")
	}
	fresh, err := Rotate(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := src(); got != fresh {
		t.Fatalf("FileSource = %q after rotation, want the new token", got)
	}
}

// TestHomeIsMadePrivate: an existing, looser $MADS_HOME is tightened to 0700.
func TestHomeIsMadePrivate(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mads")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(home); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(home); fi.Mode().Perm() != 0o700 {
		t.Fatalf("$MADS_HOME mode %04o, want 0700", fi.Mode().Perm())
	}
	if err := os.Chmod(home, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := Rotate(home); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(home); fi.Mode().Perm() != 0o700 {
		t.Fatalf("$MADS_HOME mode %04o after rotate, want 0700", fi.Mode().Perm())
	}
}

func TestTakeChildEnvRemovesBothTokens(t *testing.T) {
	t.Setenv(EnvLocalToken, "local")
	t.Setenv(EnvRouterToken, "scoped")
	l, r := TakeChildEnv()
	if l != "local" || r != "scoped" {
		t.Fatalf("TakeChildEnv = %q, %q", l, r)
	}
	for _, kv := range os.Environ() {
		if IsTokenVar(kv) {
			t.Fatalf("%s still in the environment", kv)
		}
	}
	got := ScrubEnv([]string{"PATH=/bin", "MADS_LOCAL_TOKEN=x", "MADS_ROUTER_TOKEN=y", "MADS_LOCAL_TOKENS=keep", "MADS_ROUTER_TOKEN"})
	if strings.Join(got, ",") != "PATH=/bin,MADS_LOCAL_TOKENS=keep" {
		t.Fatalf("ScrubEnv = %v", got)
	}
}

func TestRegistryScopesAndRevokes(t *testing.T) {
	g := NewRegistry()
	a1, _ := g.Mint("ses_a")
	a2, _ := g.Mint("ses_a")
	b, _ := g.Mint("ses_b")
	if id, ok := g.Lookup(a1); !ok || id != "ses_a" {
		t.Fatalf("Lookup(a1) = %q, %v", id, ok)
	}
	g.Revoke(a1)
	if _, ok := g.Lookup(a1); ok {
		t.Fatal("revoked token still valid")
	}
	if _, ok := g.Lookup(a2); !ok {
		t.Fatal("revoking one token dropped its sibling")
	}
	g.RevokeSession("ses_a")
	if _, ok := g.Lookup(a2); ok {
		t.Fatal("RevokeSession left a token")
	}
	if _, ok := g.Lookup(b); !ok {
		t.Fatal("RevokeSession dropped another session's token")
	}
	g.RevokeAll()
	if _, ok := g.Lookup(b); ok || g.Len() != 0 {
		t.Fatal("RevokeAll left a token")
	}
	if _, ok := g.Lookup("not-scoped"); ok {
		t.Fatal("an unprefixed value was looked up")
	}
}

// --- the door ---

type doorFixture struct {
	home   string
	store  *Store
	reg    *Registry
	h      http.Handler
	seen   *Principal
	logBuf *bytes.Buffer
}

// cookie presents v as a cookie — which the door never reads (H7: the
// bearer is the only way in).
func cookie(v string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "mads_local", Value: v}) }
}

func newDoorFixture(t *testing.T) *doorFixture {
	t.Helper()
	f := &doorFixture{home: t.TempDir(), reg: NewRegistry(), seen: &Principal{}}
	var err error
	if f.store, err = LoadOrCreate(f.home); err != nil {
		t.Fatal(err)
	}
	f.store.OnRotate(f.reg.RevokeAll)
	mux := http.NewServeMux()
	record := func(w http.ResponseWriter, r *http.Request) {
		*f.seen = FromContext(r.Context())
		if r.Header.Get("Authorization") != "" {
			t.Error("Authorization reached the handler")
		}
		w.WriteHeader(http.StatusTeapot)
	}
	for _, p := range []string{"GET /health", "GET /sessions", "GET /sessions/{id}/orchestrate/subjects", "/"} {
		mux.HandleFunc(p, record)
	}
	door := NewDoor(DoorOptions{
		Name:         "test",
		Store:        f.store,
		Scoped:       f.reg,
		Classify:     func(r *http.Request) string { _, p := mux.Handler(r); return p },
		Exempt:       []string{"/", "GET /health"},
		ScopedRoutes: []string{"GET /sessions/{id}/orchestrate/subjects"},
	})
	f.h = door.Wrap(mux)
	f.logBuf = &bytes.Buffer{}
	log.SetOutput(f.logBuf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return f
}

func (f *doorFixture) do(t *testing.T, method, path string, set func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	*f.seen = Principal{Kind: -1}
	r := httptest.NewRequest(method, path, nil)
	if set != nil {
		set(r)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func TestDoorRefusesWithoutAValidToken(t *testing.T) {
	f := newDoorFixture(t)
	for name, set := range map[string]func(*http.Request){
		"none":         nil,
		"wrong bearer": bearer("wrong-token-wrong-token-wrong-token-xx"),
		"empty bearer": func(r *http.Request) { r.Header.Set("Authorization", "Bearer ") },
		"basic scheme": func(r *http.Request) { r.Header.Set("Authorization", "Basic "+f.store.Token()) },
		// H7: no cookie is a credential, the install token least of all.
		"install token as a cookie": cookie(f.store.Token()),
		"install token as a port-named cookie": func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "mads_local_17373", Value: f.store.Token()})
		},
		"other cookie": func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "other", Value: f.store.Token()}) },
		"bad header, the token as a cookie": func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer wrong")
			r.AddCookie(&http.Cookie{Name: "mads_local", Value: f.store.Token()})
		},
	} {
		w := f.do(t, "GET", "/sessions", set)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: code %d, want 401", name, w.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["code"] != "unauthorized" || !strings.Contains(body["error"], "mads api") || !strings.Contains(body["error"], "local.token") {
			t.Fatalf("%s: body %s", name, w.Body)
		}
		if f.seen.Kind != -1 {
			t.Fatalf("%s: handler ran", name)
		}
	}
	if strings.Contains(f.logBuf.String(), f.store.Token()) {
		t.Fatal("a refusal log line contains the token")
	}
	if !strings.Contains(f.logBuf.String(), "refused GET /sessions") {
		t.Fatalf("refusals not logged with method and path: %s", f.logBuf)
	}
}

// TestDoorAdmitsTheInstallTokenByBearer: the bearer, any case of the
// scheme, is the one way in (H7).
func TestDoorAdmitsTheInstallTokenByBearer(t *testing.T) {
	f := newDoorFixture(t)
	for name, set := range map[string]func(*http.Request){
		"bearer":           bearer(f.store.Token()),
		"lowercase scheme": func(r *http.Request) { r.Header.Set("Authorization", "bearer "+f.store.Token()) },
	} {
		w := f.do(t, "GET", "/sessions", set)
		if w.Code != http.StatusTeapot || f.seen.Kind != Install {
			t.Fatalf("%s: code %d, principal %+v", name, w.Code, *f.seen)
		}
	}
}

func TestDoorExemptRoutesStillIdentifyTheCaller(t *testing.T) {
	f := newDoorFixture(t)
	if w := f.do(t, "GET", "/health", nil); w.Code != http.StatusTeapot || f.seen.Kind != None {
		t.Fatalf("anonymous /health: %d %+v", w.Code, *f.seen)
	}
	if w := f.do(t, "GET", "/health", bearer(f.store.Token())); w.Code != http.StatusTeapot || f.seen.Kind != Install {
		t.Fatalf("authenticated /health: %d %+v", w.Code, *f.seen)
	}
	if w := f.do(t, "GET", "/index.html", nil); w.Code != http.StatusTeapot {
		t.Fatalf("static UI: %d", w.Code)
	}
	// A method no API route takes falls to the static UI, which holds no data.
	if w := f.do(t, "DELETE", "/sessions", nil); w.Code != http.StatusTeapot || f.seen.Kind != None {
		t.Fatalf("DELETE /sessions: %d", w.Code)
	}
}

func TestDoorScopedTokens(t *testing.T) {
	f := newDoorFixture(t)
	tok, err := f.reg.Mint("ses_a")
	if err != nil {
		t.Fatal(err)
	}
	// The principal is the token's session whatever X-Mads-Actor claims;
	// the ownership check that principal feeds (a scoped token on another
	// session's routes) is pinned by services/router's
	// TestScopedTokenStaysInItsSession.
	spoof := func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-Mads-Actor", "session-ses_b")
	}
	w := f.do(t, "GET", "/sessions/ses_a/orchestrate/subjects", spoof)
	if w.Code != http.StatusTeapot || f.seen.Kind != Scoped || f.seen.SessionID != "ses_a" {
		t.Fatalf("own route: %d %+v", w.Code, *f.seen)
	}
	if w := f.do(t, "GET", "/sessions", bearer(tok)); w.Code != http.StatusForbidden {
		t.Fatalf("scoped token off its routes: %d, want 403", w.Code)
	}
	// As a cookie it is nothing.
	if w := f.do(t, "GET", "/sessions/ses_a/orchestrate/subjects", cookie(tok)); w.Code != http.StatusUnauthorized {
		t.Fatalf("scoped cookie: %d, want 401", w.Code)
	}
	// Rotation revokes it.
	if _, err := Rotate(f.home); err != nil {
		t.Fatal(err)
	}
	f.store.Token()
	if w := f.do(t, "GET", "/sessions/ses_a/orchestrate/subjects", bearer(tok)); w.Code != http.StatusUnauthorized {
		t.Fatalf("scoped token after rotation: %d, want 401", w.Code)
	}
}

func TestDoorLogsOutTheOldTokenOnRotation(t *testing.T) {
	f := newDoorFixture(t)
	old := f.store.Token()
	fresh, err := Rotate(f.home)
	if err != nil {
		t.Fatal(err)
	}
	if w := f.do(t, "GET", "/sessions", bearer(old)); w.Code != http.StatusUnauthorized {
		t.Fatalf("old token after rotation: %d", w.Code)
	}
	if w := f.do(t, "GET", "/sessions", bearer(fresh)); w.Code != http.StatusTeapot {
		t.Fatalf("new token after rotation: %d", w.Code)
	}
}

// TestDoorEndsStreamsWhenTheirCredentialDies: an open request (a stream)
// is cancelled when the credential that opened it is revoked — at once on
// rotation, within WatchEvery for a scoped token's revocation.
func TestDoorEndsStreamsWhenTheirCredentialDies(t *testing.T) {
	home := t.TempDir()
	store, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	store.OnRotate(reg.RevokeAll)
	ended := make(chan string, 4)
	mux := http.NewServeMux()
	stream := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		ended <- r.URL.Path
	}
	mux.HandleFunc("GET /events", stream)
	mux.HandleFunc("GET /sessions/{id}/orchestrate/subjects", stream)
	door := NewDoor(DoorOptions{
		Name: "test", Store: store, Scoped: reg,
		Classify:     func(r *http.Request) string { _, p := mux.Handler(r); return p },
		ScopedRoutes: []string{"GET /sessions/{id}/orchestrate/subjects"},
		WatchEvery:   20 * time.Millisecond,
	})
	srv := httptest.NewServer(door.Wrap(mux))
	defer srv.Close()
	open := func(path string, set func(*http.Request)) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		set(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("open %s: %v %v", path, resp, err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
	}
	wait := func(what string) {
		t.Helper()
		select {
		case <-ended:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: the stream outlived its credential", what)
		}
	}

	scoped, _ := reg.Mint("ses_a")
	open("/sessions/ses_a/orchestrate/subjects", bearer(scoped))
	reg.Revoke(scoped)
	wait("scoped token revoked")

	open("/events", bearer(store.Token()))
	if _, err := Rotate(home); err != nil {
		t.Fatal(err)
	}
	store.Token() // whoever notices first: the next request, or the watcher
	wait("install token rotated")
}

func TestInsecureDoorOnlyInTests(t *testing.T) {
	if !InsecureAllowed() {
		t.Fatal("a go test binary must allow the insecure door")
	}
	h := Insecure().Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if FromContext(r.Context()).Kind != Install {
			t.Error("insecure door didn't mark the caller")
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
}

func TestTransportSendsTheBearer(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
	}))
	defer srv.Close()
	c := &http.Client{Transport: &Transport{Source: func() string { return "abc" }}}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got != "Bearer abc" {
		t.Fatalf("Authorization = %q", got)
	}
}

// TestTighteningHomeIsLoggedAndAnIgnoredChmodIsAnError (round 2): making
// $MADS_HOME private is logged, and a filesystem that silently ignores
// chmod (some network and FAT mounts) is a clear error, not a quietly
// world-readable home.
func TestTighteningHomeIsLoggedAndAnIgnoredChmodIsAnError(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	home := filepath.Join(t.TempDir(), "mads")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(home); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "tightened "+home+" from mode 0755 to 0700") {
		t.Fatalf("tightening wasn't logged: %q", buf.String())
	}
	other := filepath.Join(t.TempDir(), "mads")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	old := chmod
	chmod = func(string, os.FileMode) error { return nil } // a filesystem that ignores it
	t.Cleanup(func() { chmod = old })
	_, err := LoadOrCreate(other)
	if err == nil || !strings.Contains(err.Error(), "ignores permissions") {
		t.Fatalf("an ignored chmod: %v, want a clear error", err)
	}
}
