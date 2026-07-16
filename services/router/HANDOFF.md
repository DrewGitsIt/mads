# HANDOFF: Session detail + transcript endpoints

## Problem

`ui/src/router/client.ts`'s `FetchRouterClient.getSession`/`getTranscript`
call `GET /sessions/{id}` and `GET /sessions/{id}/transcript`. Neither route
is registered in `services/router/internal/api/api.go`. `ConversationTab`
(`ui/src/conversation/ConversationTab.tsx:44-55`) awaits `getSession` and
renders `null` until it resolves — with no route, the fetch 404s (falls
through to the SPA's `index.html` catch-all, not even a clean JSON error),
the promise never resolves into a session, and the pane stays permanently
blank. Both routes were built and tested only against `FakeRouterClient`;
they never existed on the wire.

## Decision: the UI's contract is right, extend `pkg/contracts` to match

`ui/src/router/types.ts` already hand-mirrors the exact shape the backend
already produces elsewhere:

- `Session` (`types.ts:8-29`) mirrors `contracts.Session` (`pkg/contracts/
  session.go:57-104`) field-for-field — nothing to reconcile there, `GET
  /sessions/{id}` just needs to exist and return `contracts.Session` as JSON,
  exactly like `GET /sessions` (list) already does.
- `TranscriptMessage`/`ToolUse` (`types.ts:211-215`, `197-201`) mirror
  `provider.Message`/`provider.ToolUse` (`providers/provider/event.go:38-65`)
  — the normalized shape `TranscriptReader.Transcript` already returns, and
  which `providers/fake`, `providers/claudecode`, `providers/kirocli` all
  already implement and test against. **`pkg/contracts` has no equivalent
  type today** — that's the actual gap. Add it there (not just reuse
  `provider.Message` inline), matching this task's "wire types in
  pkg/contracts" convention and the existing pattern of `contracts.FileChange`
  / `contracts.Commit` being Router-owned mirrors of Session-Service-produced
  data.

No UI change is needed. This is purely additive on the Go side.

## Design

### 1. `pkg/contracts/transcript.go` (new file)

```go
package contracts

import "encoding/json"

// ToolUse mirrors providers/provider/event.go ToolUse — the wire shape of a
// normalized tool invocation inside a transcript message. Duplicated here
// (not imported from providers/provider) so pkg/contracts stays free of a
// dependency on the provider layer, same as every other cross-boundary type
// in this package.
type ToolUse struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input,omitempty"`
	Path  string          `json:"path,omitempty"`
}

// TranscriptMessage mirrors providers/provider/event.go Message — one
// normalized transcript entry, as returned by GET /sessions/{id}/transcript.
type TranscriptMessage struct {
	Role string   `json:"role"` // "user", "agent", or "tool"
	Text string   `json:"text,omitempty"`
	Tool *ToolUse `json:"tool,omitempty"`
}
```

Field names/optionality must match `ui/src/router/types.ts` exactly (they
already do, by construction — the UI was built ahead of this route per its
own doc comments). No golden fixture needed — `contracts.Session`,
`FileChange`, `Commit`, `MergeResult` set the precedent that not every type
in this package has one (only `task.go`, `metrics.go`, `event.go`,
`apierror.go` do today).

### 2. `GET /sessions/{id}` — `services/router/internal/api/sessions.go`

Pure proxy, same shape as every other single-resource read in this package:

```go
func (s *server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sess)
}
```

`dataclient.Client.Session` (`pkg/dataclient/dataclient.go:223`) already
exists and does exactly this lookup — no Data Service or dataclient change
needed. `writeError` already maps `dataclient.ErrNotFound` to a 404 JSON
body (`api.go:196-208`), which is the acceptance criterion's "404 JSON for
unknown ids" for free — don't hand-roll it.

Register in `api.go`'s route table, near the other `/sessions/{id}...`
lines:

```go
mux.HandleFunc("GET /sessions/{id}", s.handleGetSession)
```

Registration order doesn't matter — Go 1.22's `net/http` mux always ranks a
more specific pattern (`/sessions/{id}/output`) above a less specific one
(`/sessions/{id}`) regardless of registration order (already relied on
elsewhere; see README's static-handler section and
`TestStaticDoesNotShadowAPIRoutes`).

### 3. `GET /sessions/{id}/transcript` — new file `services/router/internal/api/transcript.go`

**Core decision, and why it's simpler than it first looks:** the scope note
says to look at `handleSessionOutput`'s live-child reverse-proxy pattern.
Do — then don't reuse it here. `/output` proxies because that data (raw
streaming provider events) exists *only* in the live child process's memory
and is intentionally never persisted (README: "Data plane: forwarded, never
persisted"). Transcript is the opposite case: `contracts.Session.ProviderRef`
(`session.go:97-101`) is explicitly documented as "the transcript pointer:
`TranscriptReader.Transcript(ctx, ref)` reads by this same value" — i.e. the
transcript is meant to be read directly off the persisted ref, independent
of whether a child is currently running. Concretely:

- `claudecode.Provider.Transcript` and `kirocli`'s equivalent both read an
  on-disk file keyed by `ref` (`providers/claudecode/transcript.go:20-37`) —
  this works identically whether the session is active or not, and whether
  it's read from the Router's process or the Session Service's, since it's
  filesystem state, not process memory.
- `providers/fake`'s `Transcript` reads an in-memory map keyed by `ref`
  (`providers/fake/fake.go:160-168`), populated live as `Send`/respond run
  (`fake.go:222-226, 397-398`) — so it reflects an in-progress conversation
  too, not just a finished one.

So: **no branching on `sess.Activity`, no supervisor/child lookup, no
reverse proxy.** The Router already holds a `*provider.Registry` (`s.providers`,
the same instance used for `handleCreateSession`'s pre-check) — read straight
from that, in-process:

```go
// transcriptFor reads sess's durable transcript via its bound provider,
// keyed by the persisted ProviderRef (contracts/session.go's documented
// "transcript pointer"). Never errors outward — every failure mode here
// (no conversation yet, provider deregistered, provider doesn't implement
// TranscriptReader, the read itself fails) degrades to an empty transcript,
// matching this endpoint's "always valid JSON, never a blank pane" contract.
func (s *server) transcriptFor(ctx context.Context, sess contracts.Session) []contracts.TranscriptMessage {
	out := []contracts.TranscriptMessage{}
	if sess.ProviderRef == "" {
		return out // pipeline-only session (e.g. "Wave 7"): no interactive conversation ever started
	}
	p, err := s.providers.Get(sess.Provider)
	if err != nil {
		log.Printf("api: transcript: provider %q: %v", sess.Provider, err)
		return out
	}
	reader, ok := p.(provider.TranscriptReader)
	if !ok {
		return out
	}
	msgs, err := reader.Transcript(ctx, sess.ProviderRef)
	if err != nil {
		log.Printf("api: transcript: read session %s: %v", sess.ID, err)
		return out
	}
	result := make([]contracts.TranscriptMessage, len(msgs))
	for i, m := range msgs {
		tm := contracts.TranscriptMessage{Role: m.Role, Text: m.Text}
		if m.Tool != nil {
			tm.Tool = &contracts.ToolUse{Name: m.Tool.Name, Input: m.Tool.Input, Path: m.Tool.Path}
		}
		result[i] = tm
	}
	return result
}

func (s *server) handleSessionTranscript(w http.ResponseWriter, r *http.Request) {
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.transcriptFor(r.Context(), sess))
}
```

`out := []contracts.TranscriptMessage{}` (not a nil slice) so `writeJSON`
encodes `[]`, never `null` — satisfies "a session with no conversation ...
returns an empty array, NOT an error" precisely, and is friendlier to the UI
than `null` would be (`ChatRenderer` maps over `messages` directly).

Register:

```go
mux.HandleFunc("GET /sessions/{id}/transcript", s.handleSessionTranscript)
```

Needs `"log"` and `"github.com/drewkidwell/mads/providers/provider"` added
to this new file's imports (`sessions.go`'s existing imports — `context`,
`net/http`, `contracts` — are insufficient on their own).

### The four required cases, mapped to this design

| Case | `sess.ProviderRef` | Result |
|---|---|---|
| Active, has messages (fake provider) | set, fake's in-memory history populated | `reader.Transcript` returns the recorded messages |
| Inactive | set (persisted from when it was active) | same path, same result — `Activity` is never consulted |
| No conversation (pipeline-only, e.g. "Wave 7") | `""` (interactive provider session never started) | empty array, first branch |
| Unknown session id | n/a | `s.data.Session` returns `dataclient.ErrNotFound` → `writeError` → 404 JSON, before `transcriptFor` ever runs |

## Test plan — `services/router/internal/api/transcript_test.go` (new)

Follow the existing `newTestServerWithData(t) (*httptest.Server, string)`
pattern (`api_test.go`/`events_test.go`) for direct Data Service writes, but
this task needs a *specific, held* `*fake.Fake` instance — not a fresh one
from `testProviders(t)` — so a test can drive a real conversation through it
and then assert the Router reads that same history back. Add a small local
variant in the new test file rather than changing `testOptions`/
`testProviders` (used by every other test in the package; don't perturb
their behavior):

```go
func newTestServerWithFake(t *testing.T, fp *fake.Fake) (*httptest.Server, string) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	reg := provider.NewRegistry()
	if err := reg.Register(fp); err != nil {
		t.Fatal(err)
	}
	opts := testOptions(t, data, sup)
	opts.Providers = reg
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	return srv, base
}
```

Then, for the active/inactive cases: call `fp.Start(ctx, provider.StartOptions{Mode:
provider.ModeACP})` directly, `provSess.Send(ctx, "echo hi")`, drain
`provSess.Events()` until `EventTurnEnd` (so both the user and agent
messages are recorded — `fake.go`'s `Send` records the user message
synchronously but the agent reply only in `finish`, at turn end), then
`data.CreateSession(ctx, &contracts.Session{Name: ..., Provider: "fake",
ProviderRef: provSess.Ref(), Activity: contracts.SessionActive /* or
Inactive */})` via a `dataclient.Client` built from the returned data base
URL — bypassing `POST /sessions` entirely, since that would try to spawn a
real child through the supervisor (this endpoint doesn't need one).

- **`TestSessionTranscriptActiveWithMessages`** — as above with
  `Activity: SessionActive`. `GET /sessions/{id}/transcript` → 200, body has
  2 messages: `{role: "user", text: "echo hi"}`, `{role: "agent", text: "you
  said hi"}` (fake's built-in `echo ` dialect).
- **`TestSessionTranscriptInactive`** — identical setup, `Activity:
  SessionInactive`. Same 2 messages — proves `Activity` isn't consulted.
- **`TestSessionTranscriptNoConversation`** — `data.CreateSession` with no
  `ProviderRef` set (zero value). `GET .../transcript` → 200, body `[]`
  (assert via `decodeJSON[[]contracts.TranscriptMessage]` and `len(...) ==
  0`, and separately assert the raw response body is the literal 2-byte
  `[]`, not `null`, to pin the "empty array, not null" guarantee).
- **`TestSessionTranscriptUnknownID`** — `GET /sessions/nonexistent/transcript`
  → 404, JSON body with an `"error"` field (same shape as
  `TestRetryTaskNotFound`'s 404 assertion).

Also add to `sessions_test.go`:

- **`TestGetSession`** — create via `data.CreateSession` directly, `GET
  /sessions/{id}` → 200, decoded `contracts.Session` matches.
- **`TestGetSessionNotFound`** — `GET /sessions/nonexistent` → 404 JSON.

## `services/router/README.md`

Add two entries to the `## API contract` bullet list, near the existing
`GET /sessions/{id}/files` line:

- `GET /sessions/{id}` — the session record (proxies `dataclient.Session`);
  404 if unknown.
- `GET /sessions/{id}/transcript` — the session's durable conversation
  history, read directly from its bound provider via the persisted
  `ProviderRef` (`Session.ProviderRef`'s documented "transcript pointer"),
  **not** proxied through a running child the way `/output` is — this works
  identically whether the session is active or inactive. Always 200 with a
  JSON array (`[]` for a session with no interactive conversation, e.g. a
  pipeline-only run, or a provider that doesn't implement transcript
  reading); 404 only for an unknown session id.

## Risks / things the reviewer should scrutinize

- **The `fake` provider's history does not cross process boundaries.**
  `services/router/main.go` and `services/session/main.go` each construct
  their own `fake.New()` instance independently — they are not shared. In a
  real dev/demo run (not this task's tests, which deliberately share one
  instance), a live conversation running against the `fake` provider inside
  the Session Service child is invisible to the Router's own `fake`
  instance, so `GET .../transcript` would incorrectly return `[]` for an
  active fake-provider session outside of tests. This is a pre-existing gap
  in how `fake` is wired (not introduced by this task) and is out of scope
  to fix here — flagging it so it isn't mistaken for a bug in this change.
  `claude-code`/`kirocli` are unaffected (disk-backed, not process-memory-backed).
- **`reader.Transcript` errors are swallowed to `[]`, not surfaced.** Chosen
  deliberately (matches this codebase's "never a hard error, always data"
  convention already used by `/preflight` and `POST /update/check`) so a
  transient read failure never reintroduces the blank-pane bug this task
  fixes. A reviewer who wants failures visible could argue for logging a
  `session.transcript_read_failed`-style signal instead of a bare
  `log.Printf`; not done here since nothing else in this package emits
  events from a GET handler.

## Verification

1. `make check` (root) — lint + `go test -race ./...` + UI lint + UI test.
2. Real-stack check (`scripts/app.sh` or `make run` + `npm --prefix ui run
   build`/`start`): create a session with the `fake` provider, send a
   message, click into it from the session list — the Conversation tab's
   header must render (not a blank pane), and the sent message plus its
   reply must appear in the chat pane. Also click into an existing
   pipeline-only session (no prior interactive messages) and confirm it
   renders with an empty transcript, not an error state.

## Executor notes (implementation, as actually landed)

Implemented per the design above with no deviation, plus one unplanned fix
and one scope boundary discovered during real-stack verification:

- `pkg/contracts/transcript.go`, `handleGetSession`, `transcript.go`
  (`transcriptFor` + `handleSessionTranscript`), the two route registrations
  in `api.go`, `transcript_test.go`'s four cases, and `sessions_test.go`'s
  `TestGetSession`/`TestGetSessionNotFound` all landed exactly as designed
  above.
- **Unplanned fix: `static_test.go`'s `TestStaticSPAFallback`.** It used `GET
  /sessions/abc123` as its example of "an unregistered path the SPA catch-all
  must serve `index.html` for." That path is no longer unregistered — `GET
  /sessions/{id}` now correctly 404s it as a real API route (unknown session
  id), which is the intended behavior, but broke this test's premise. Fixed
  by switching the example to `/some/unknown/route` (matching the analogous
  test in `internal/static/static_test.go`) and updating the comment.
  Confirmed via `ui/src/App.tsx` that the UI has no client-side URL routing
  for sessions (session selection is local `useState`, no react-router), so
  `/sessions/abc123` was never a real browser-navigated path — just an
  arbitrary stand-in the test no longer gets to use. **Reviewer: worth a
  second look**, since it's the one change outside this doc's original plan.
- `make check` is green: `go build`/`go vet` clean, `go test -race ./...`
  passes (all packages), `golangci-lint run ./...` reports 0 issues,
  `npm --prefix ui run lint` and `npm --prefix ui run test` (707 tests) pass.

### Real-stack verification (done, with one adjustment from the plan above)

Built and ran real `data`/`router`/`session` binaries (isolated data dir and
ports, not the shared dev instance) and drove them with `curl` rather than
through Electron/a browser — no browser automation tool is available in this
environment. Confirmed, against the compiled binaries:

- `POST /sessions` (provider `fake`) → `GET /sessions/{id}` → 200 with the
  full `contracts.Session` JSON (matches `ui/src/router/types.ts`'s
  `Session`).
- `GET /sessions/{id}/transcript` on that fresh session → 200, body `[]`.
- `GET /sessions/nonexistent` and `GET /sessions/nonexistent/transcript` →
  both 404 JSON (`{"error": ...}`).

This directly verifies the acceptance criterion's mechanism: `ConversationTab`
(`ui/src/conversation/ConversationTab.tsx:44-55`) renders `null` until
`getSession` resolves; it now resolves (200, real data) instead of hanging
forever on an unregistered-route fetch failure, so the header renders instead
of a permanent blank pane.

**Could not verify, and flagging why:** driving a live conversation through
the real stack (send a message, confirm it and the reply appear via
`/transcript`) turned out to be blocked by a separate, pre-existing gap, not
by this task's endpoints — `POST /sessions/{id}/messages` (the route
`RouterClient.sendMessage` calls) **is not registered in `api.go` at all**.
Sending a message through the real HTTP API 404s to the SPA fallback today,
independent of anything in this change. This is out of this task's scope
(only `GET /sessions/{id}` and `GET /sessions/{id}/transcript` were
assigned), but the reviewer/next task should know: a real end-to-end
"type a message, see the reply" click-through is not yet possible against
this Router build, for reasons unrelated to this task. The unit tests'
approach of driving `fake.Fake` directly in-process (bypassing HTTP) rather
than through `POST /sessions/{id}/messages` was the correct call, not a
shortcut — it's the only way to exercise this today.

Also could not verify the "active with messages" / "inactive" transcript
cases against the real stack specifically (only via the unit tests) — the
fake provider's history doesn't cross process boundaries (see Risks above),
so there's no way to seed history into the Router's own in-process `fake`
instance from outside it via curl. This is the same documented limitation,
not a new one.
