package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// importFixture is a fake ~/.claude tree plus a Router whose claude-code
// provider reads it — the real ~/.claude is never touched.
type importFixture struct {
	srv        *httptest.Server
	data       *dataclient.Client
	sup        *supervisor.Supervisor
	projects   string
	live       string
	home       string
	repo       string
	missingDir string
}

const (
	impT1 = "2026-09-01T10:00:00.000Z"
	impT2 = "2026-09-01T10:05:00.000Z"
)

func (f *importFixture) native(t *testing.T, project, id string, lines ...string) {
	t.Helper()
	path := filepath.Join(f.projects, project, id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// convo is a two-line conversation (one user turn, one reply) in cwd.
func convo(cwd, entry, text string) []string {
	return []string{
		fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":%q,"cwd":%q,"entrypoint":%q,"message":{"role":"user","content":%q}}`, impT1, cwd, entry, text),
		fmt.Sprintf(`{"type":"assistant","uuid":"a1","timestamp":%q,"cwd":%q,"entrypoint":%q,"message":{"id":"m1","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"reply to %s"}],"usage":{"input_tokens":4,"output_tokens":9,"cache_read_input_tokens":1000,"cache_creation_input_tokens":50}}}`, impT2, cwd, entry, text),
	}
}

func newImportFixture(t *testing.T) *importFixture {
	t.Helper()
	f := &importFixture{
		projects: filepath.Join(t.TempDir(), "projects"),
		live:     filepath.Join(t.TempDir(), "sessions"),
		home:     t.TempDir(),
		repo:     filepath.Join(t.TempDir(), "repo"),
	}
	f.missingDir = filepath.Join(t.TempDir(), "deleted-folder")
	for _, d := range []string{f.live, filepath.Join(f.repo, "api"), filepath.Join(f.repo, "ui")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", f.home)

	base := datatest.Start(t)
	f.data = dataclient.New(base, "router")
	f.sup = supervisor.New(f.data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { f.sup.Shutdown(context.Background()) })
	reg := provider.NewRegistry()
	if err := reg.Register(claudecode.New(claudecode.Config{
		TranscriptDir: f.projects, LiveSessionsDir: f.live, HistoryFile: filepath.Join(t.TempDir(), "history.jsonl"),
	})); err != nil {
		t.Fatal(err)
	}
	opts := testOptions(t, f.data, f.sup)
	opts.Providers = reg
	f.srv = httptest.NewServer(api.New(opts))
	t.Cleanup(f.srv.Close)
	return f
}

// populate writes the acceptance tree: 8 importable conversations and 5
// that must be left out.
func (f *importFixture) populate(t *testing.T) {
	t.Helper()
	// Importable.
	normal := convo(f.repo, "cli", "fix the login bug")
	f.native(t, "-repo", "normal", append(normal, `{"type":"ai-title","aiTitle":"Login bug fix"}`)...)
	f.native(t, "-repo-api", "in-api", convo(filepath.Join(f.repo, "api"), "cli", "add an endpoint")...)
	f.native(t, "-repo-ui", "in-ui", convo(filepath.Join(f.repo, "ui"), "cli", "restyle the rail")...)
	f.native(t, "-gone", "missing", convo(f.missingDir, "cli", "work in a folder since deleted")...)
	f.native(t, "-", "root", convo("/", "cli", "poke around the filesystem")...)
	f.native(t, "-home", "home", convo(f.home, "cli", "tidy my dotfiles")...)
	f.native(t, "-repo", "stale-reg", convo(f.repo, "cli", "its process is gone")...)
	f.native(t, "-repo", "dupe", convo(f.repo, "cli", "already a mads session")...)
	// Left out.
	f.native(t, "-repo", "sdk", convo(f.repo, "sdk-cli", "mads agent run")...)
	f.native(t, "-repo", "running", convo(f.repo, "cli", "open in a terminal right now")...)
	f.native(t, "-repo", "empty", `{"type":"ai-title","aiTitle":"never said anything"}`, `{"type":"attachment","cwd":"/x"}`)
	f.native(t, filepath.Join("-repo", "normal"), "subagents/agent-1", convo(f.repo, "cli", "subagent work")...)
	f.native(t, "-repo", "meta-only", fmt.Sprintf(`{"type":"user","timestamp":%q,"cwd":%q,"isMeta":true,"message":{"role":"user","content":"Caveat: x"}}`, impT1, f.repo))

	reg := func(file, id string, pid int) {
		body := fmt.Sprintf(`{"sessionId":%q,"cwd":%q,"pid":%d,"status":"idle"}`, id, f.repo, pid)
		if err := os.WriteFile(filepath.Join(f.live, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg("live.json", "running", os.Getpid())
	reg("stale.json", "stale-reg", 2147483646)

	// mads already has "dupe" as a session's provider_ref — a session that has
	// run, so the runtime told mads its model's real window.
	if err := f.data.CreateSession(context.Background(), &contracts.Session{
		Name: "already here", Provider: "claude-code", ProviderRef: "dupe", Activity: contracts.SessionInactive,
		LastUsage: &contracts.Usage{Model: "claude-opus-5", ContextTokens: 5000, ContextWindow: 1_000_000},
	}); err != nil {
		t.Fatal(err)
	}
}

type scanResp struct {
	Providers []struct {
		Provider string `json:"provider"`
		Count    int    `json:"count"`
	} `json:"providers"`
	Folders []struct {
		Dir    string `json:"dir"`
		Count  int    `json:"count"`
		Exists bool   `json:"exists"`
	} `json:"folders"`
	Total           int      `json:"total"`
	AlreadyImported int      `json:"already_imported"`
	LookedFor       []string `json:"looked_for"`
}

func (f *importFixture) scan(t *testing.T) scanResp {
	t.Helper()
	resp, err := http.Get(f.srv.URL + "/imports/conversations")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	return decodeJSON[scanResp](t, resp)
}

func (f *importFixture) importAll(t *testing.T) map[string]int {
	t.Helper()
	resp := postJSON(t, f.srv.URL+"/imports/conversations", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST status = %d", resp.StatusCode)
	}
	return decodeJSON[map[string]int](t, resp)
}

func TestImportConversationsScan(t *testing.T) {
	f := newImportFixture(t)
	f.populate(t)

	got := f.scan(t)
	if got.Total != 7 || got.AlreadyImported != 1 {
		t.Errorf("total=%d already_imported=%d, want 7 and 1", got.Total, got.AlreadyImported)
	}
	if len(got.Providers) != 1 || got.Providers[0].Provider != "claude-code" || got.Providers[0].Count != 7 {
		t.Errorf("providers = %+v", got.Providers)
	}
	counts := map[string]int{}
	exists := map[string]bool{}
	for _, fo := range got.Folders {
		counts[fo.Dir], exists[fo.Dir] = fo.Count, fo.Exists
	}
	want := map[string]int{f.repo: 2, filepath.Join(f.repo, "api"): 1, filepath.Join(f.repo, "ui"): 1, f.missingDir: 1, "/": 1, f.home: 1}
	if len(counts) != len(want) {
		t.Errorf("folders = %v, want %v", counts, want)
	}
	for dir, n := range want {
		if counts[dir] != n {
			t.Errorf("folder %s count = %d, want %d", dir, counts[dir], n)
		}
	}
	if exists[f.missingDir] || !exists[f.repo] || !exists["/"] {
		t.Errorf("exists = %v, want the deleted folder false and the others true", exists)
	}
	if !slices.Contains(got.LookedFor, f.projects) {
		t.Errorf("looked_for = %v, want it to name %s", got.LookedFor, f.projects)
	}
	// A GET writes nothing.
	all, _ := f.data.Sessions(context.Background(), "")
	if len(all) != 1 {
		t.Errorf("GET created sessions: %d total, want just the pre-existing one", len(all))
	}
}

func TestImportConversationsCreatesDormantRecords(t *testing.T) {
	f := newImportFixture(t)
	f.populate(t)
	ctx := context.Background()
	// A workspace whose repo contains two of the conversations' folders.
	ws := &contracts.Workspace{Name: "main", Repos: []contracts.WorkspaceRepo{{Path: f.repo, Primary: true}}}
	if err := f.data.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}

	if res := f.importAll(t); res["imported"] != 7 || res["skipped_existing"] != 1 {
		t.Fatalf("POST = %v, want imported 7, skipped_existing 1", res)
	}
	all, err := f.data.Sessions(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	byRef := map[string]contracts.Session{}
	for _, s := range all {
		byRef[s.ProviderRef] = s
	}
	if len(all) != 8 { // 7 imported + the pre-existing "dupe"
		t.Fatalf("%d sessions after import, want 8", len(all))
	}
	for _, ref := range []string{"sdk", "running", "empty", "meta-only", "agent-1"} {
		if _, ok := byRef[ref]; ok {
			t.Errorf("conversation %q was imported, want it skipped", ref)
		}
	}

	n := byRef["normal"]
	if n.Name != "Login bug fix" || n.NameSource != contracts.SessionNameSourceGenerated ||
		n.Activity != contracts.SessionInactive || n.Isolation != contracts.IsolationCurrentDir ||
		n.Provider != "claude-code" || !n.ForkOnResume || n.ImportedFrom != "normal" ||
		!slices.Equal(n.Tags, []string{"imported"}) || n.Dir != f.repo || n.Workspace != ws.ID {
		t.Errorf("imported session = %+v", n)
	}
	if n.Trust != contracts.TrustLimited {
		t.Errorf("trust = %q, want the built-in default %q", n.Trust, contracts.TrustLimited)
	}
	created, _ := time.Parse(time.RFC3339Nano, impT1)
	last, _ := time.Parse(time.RFC3339Nano, impT2)
	if !n.CreatedAt.Equal(created) {
		t.Errorf("created_at = %v, want the first native timestamp %v", n.CreatedAt, created)
	}
	if n.LastTurnAt == nil || !n.LastTurnAt.Equal(last) {
		t.Errorf("last_turn_at = %v, want %v", n.LastTurnAt, last)
	}
	// The window is the one the runtime reported for this model on the
	// "dupe" session, not the static 200k table.
	if u := n.LastUsage; u == nil || u.Model != "claude-opus-5" || u.OutputTokens != 9 || u.ContextTokens != 1054 ||
		u.ContextWindow != 1_000_000 {
		t.Errorf("last_usage = %+v", u)
	}
	// Untitled conversations are named from the first user message.
	if got := byRef["in-api"].Name; got != "add an endpoint" {
		t.Errorf("name = %q, want the first user message", got)
	}

	// Workspace only where an existing workspace's repo contains the folder.
	for ref, wantWS := range map[string]string{
		"normal": ws.ID, "in-api": ws.ID, "in-ui": ws.ID, "stale-reg": ws.ID,
		"missing": "", "root": "", "home": "",
	} {
		if byRef[ref].Workspace != wantWS {
			t.Errorf("%s workspace = %q, want %q", ref, byRef[ref].Workspace, wantWS)
		}
	}
	// Folders are taken as-is, including `/`, `~` and a deleted one.
	for ref, dir := range map[string]string{"root": "/", "home": f.home, "missing": f.missingDir} {
		if byRef[ref].Dir != dir {
			t.Errorf("%s dir = %q, want %q", ref, byRef[ref].Dir, dir)
		}
	}

	// Nothing was created but sessions, and nothing was spawned.
	wss, _ := f.data.Workspaces(ctx)
	if len(wss) != 1 {
		t.Errorf("%d workspaces after import, want only the one the test made", len(wss))
	}
	for _, s := range all {
		if _, running := f.sup.SessionPID(s.ID); running {
			t.Errorf("session %s has a child process after import", s.ID)
		}
		if s.Activity != contracts.SessionInactive {
			t.Errorf("session %s activity = %q, want inactive", s.ID, s.Activity)
		}
	}
}

// With no session that has reported this model's window, an import can't
// know what the native token counts are out of — the 200k fallback would
// overstate a 1M session fivefold and auto-compact it on its first continue.
// So it records no usage at all: unknown, not wrong.
func TestImportConversationsWithoutKnownWindowRecordsNoUsage(t *testing.T) {
	f := newImportFixture(t)
	f.native(t, "-repo", "lone", convo(f.repo, "cli", "a conversation from before mads")...)

	if res := f.importAll(t); res["imported"] != 1 {
		t.Fatalf("POST = %v, want imported 1", res)
	}
	all, err := f.data.Sessions(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ProviderRef != "lone" {
		t.Fatalf("sessions = %+v, want just the imported one", all)
	}
	if all[0].LastUsage != nil {
		t.Errorf("last_usage = %+v, want none without a runtime-reported window", all[0].LastUsage)
	}
}

func TestImportConversationsIsIdempotent(t *testing.T) {
	f := newImportFixture(t)
	f.populate(t)
	ctx := context.Background()
	f.importAll(t)

	if res := f.importAll(t); res["imported"] != 0 || res["skipped_existing"] != 8 {
		t.Fatalf("second POST = %v, want nothing new and 8 skipped", res)
	}
	if got := f.scan(t); got.Total != 0 || got.AlreadyImported != 8 {
		t.Errorf("GET after import: total=%d already_imported=%d, want 0 and 8", got.Total, got.AlreadyImported)
	}

	// The first wake forks: provider_ref becomes the fork's id and the flag
	// clears. The original is still recognized as imported.
	all, _ := f.data.Sessions(ctx, "")
	var woken contracts.Session
	for _, s := range all {
		if s.ImportedFrom == "normal" {
			woken = s
		}
	}
	woken.ProviderRef, woken.ForkOnResume = "forked-id", false
	if err := f.data.UpdateSession(ctx, &woken); err != nil {
		t.Fatal(err)
	}
	if res := f.importAll(t); res["imported"] != 0 {
		t.Errorf("POST after a fork = %v, want the forked original not re-imported", res)
	}
	// A full-record PUT from a client that doesn't know the field keeps it.
	woken.ImportedFrom = ""
	if err := f.data.UpdateSession(ctx, &woken); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.data.Session(ctx, woken.ID); got.ImportedFrom != "normal" {
		t.Errorf("imported_from after PUT = %q, want it preserved", got.ImportedFrom)
	}
	// New conversations appearing later are picked up.
	f.native(t, "-repo", "later", convo(f.repo, "cli", "written after the first import")...)
	if res := f.importAll(t); res["imported"] != 1 {
		t.Errorf("POST after a new conversation = %v, want 1 imported", res)
	}
}

func TestImportedSessionTranscriptComesFromTheNativeFile(t *testing.T) {
	f := newImportFixture(t)
	f.native(t, "-repo", "normal", convo(f.repo, "cli", "fix the login bug")...)
	f.importAll(t)
	all, _ := f.data.Sessions(context.Background(), "")
	if len(all) != 1 {
		t.Fatalf("%d sessions, want 1", len(all))
	}

	resp, err := http.Get(f.srv.URL + "/sessions/" + all[0].ID + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	msgs := decodeJSON[[]contracts.TranscriptMessage](t, resp)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].Text != "fix the login bug" ||
		msgs[1].Role != "agent" || msgs[1].Text != "reply to fix the login bug" {
		t.Errorf("transcript = %+v, want the native conversation", msgs)
	}
}

func TestImportConversationsEmpty(t *testing.T) {
	f := newImportFixture(t)
	got := f.scan(t)
	if got.Total != 0 || got.Providers == nil || got.Folders == nil || len(got.LookedFor) == 0 {
		t.Errorf("empty scan = %+v, want zero total, empty (non-null) lists, and looked_for", got)
	}
	if res := f.importAll(t); res["imported"] != 0 {
		t.Errorf("POST over nothing = %v", res)
	}
}

// A client can't backdate a session created through POST /sessions.
func TestCreateSessionIgnoresClientCreatedAt(t *testing.T) {
	srv := newTestServer(t)
	resp := postJSON(t, srv.URL+"/sessions", map[string]any{
		"name": "fresh", "provider": "fake", "created_at": "2001-01-01T00:00:00Z", "imported_from": "x",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp)
	if created.Session.CreatedAt.Year() < 2020 || created.Session.ImportedFrom != "" {
		t.Errorf("created_at=%v imported_from=%q, want server-stamped and empty", created.Session.CreatedAt, created.Session.ImportedFrom)
	}
}

// An imported Claude Code conversation continues at the level matching the
// permission mode it last ran in (claudecode trustFromPermissionMode): auto
// → auto_review, plan → read_only, bypassPermissions → full, the latest
// mode winning over earlier ones. One with no recorded mode — or one mads
// has no level for — takes the configured default, which here is set away
// from the built-in limited to prove it's the config that applies.
func TestImportConversationsTrustFromPermissionMode(t *testing.T) {
	f := newImportFixture(t)
	ctx := context.Background()
	cfg, err := f.data.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultTrust = contracts.TrustReadOnly
	if err := f.data.UpdateConfig(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	modeLine := func(mode string) string {
		return fmt.Sprintf(`{"type":"permission-mode","permissionMode":%q,"sessionId":"s"}`, mode)
	}
	userIn := func(mode string) string {
		return fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":%q,"cwd":%q,"entrypoint":"cli","permissionMode":%q,"message":{"role":"user","content":"go"}}`, impT1, f.repo, mode)
	}
	f.native(t, "-repo", "was-auto", append([]string{modeLine("default"), userIn("default")}, modeLine("auto"))...)
	f.native(t, "-repo", "was-plan", userIn("plan"))
	f.native(t, "-repo", "was-bypass", userIn("bypassPermissions"))
	f.native(t, "-repo", "was-accept", userIn("acceptEdits"))
	f.native(t, "-repo", "was-dontask", userIn("dontAsk"))
	f.native(t, "-repo", "no-mode", convo(f.repo, "cli", "older transcript")...)

	if res := f.importAll(t); res["imported"] != 6 {
		t.Fatalf("POST = %v, want 6 imported", res)
	}
	all, err := f.data.Sessions(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]contracts.SessionTrust{}
	for _, s := range all {
		got[s.ProviderRef] = s.Trust
	}
	for ref, want := range map[string]contracts.SessionTrust{
		"was-auto":    contracts.TrustAutoReview,
		"was-plan":    contracts.TrustReadOnly,
		"was-bypass":  contracts.TrustFull,
		"was-accept":  contracts.TrustLimited,
		"was-dontask": contracts.TrustReadOnly, // the configured default
		"no-mode":     contracts.TrustReadOnly, // the configured default
	} {
		if got[ref] != want {
			t.Errorf("%s: trust = %q, want %q", ref, got[ref], want)
		}
	}
}

// A delegate's own transcript is read from the harness's store: for an
// imported session, before and after continuing it — the continue forks, and
// the original's delegates stay in the original's folder.
func TestDelegateTranscriptFromTheNativeStore(t *testing.T) {
	f := newImportFixture(t)
	f.native(t, "-repo", "normal", convo(f.repo, "cli", "fix the login bug")...)
	f.native(t, filepath.Join("-repo", "normal"), "subagents/agent-a1",
		`{"type":"user","isSidechain":true,"agentId":"a1","uuid":"s1","message":{"role":"user","content":"find the login handler"}}`,
		`{"type":"assistant","isSidechain":true,"agentId":"a1","uuid":"s2","message":{"role":"assistant","content":[{"type":"text","text":"It's in auth.go."}]}}`,
	)
	f.importAll(t)
	all, _ := f.data.Sessions(context.Background(), "")
	if len(all) != 1 {
		t.Fatalf("%d sessions, want 1", len(all))
	}
	sess := all[0]

	read := func(harnessID string) contracts.DelegateTranscript {
		t.Helper()
		resp, err := http.Get(f.srv.URL + "/sessions/" + sess.ID + "/delegates/" + harnessID + "/transcript")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		return decodeJSON[contracts.DelegateTranscript](t, resp)
	}
	check := func(when string) {
		t.Helper()
		got := read("a1")
		if !got.Available || len(got.Messages) != 2 || got.Messages[0].Role != "user" ||
			got.Messages[0].Text != "find the login handler" || got.Messages[1].Text != "It's in auth.go." {
			t.Errorf("%s: delegate transcript = %+v", when, got)
		}
	}
	check("as imported")
	if got := read("nobody"); got.Available || got.Messages == nil || len(got.Messages) != 0 {
		t.Errorf("unknown delegate = %+v, want unavailable with an empty (non-null) list", got)
	}

	sess.ProviderRef, sess.ForkOnResume = "forked-ref", false
	if err := f.data.UpdateSession(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}
	check("after the continue forked it")
}
