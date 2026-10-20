# providers/

The provider layer (spec §11, §15): everything between the platform and an
agent harness. Adapters are Go packages linked into the services that spawn
agents — not services themselves.

- `provider/` — the abstraction: the `Provider`/`Session` interfaces, optional
  strategy interfaces (`ModelDiscoverer`, `TranscriptReader`), the capability
  `Matrix` (each behavior declared native/emulated/unsupported, required rows
  enforced at registration), normalized events/usage/transcript types, and the
  `Registry` the platform resolves providers from.
- `conformance/` — the §11 capability matrix as an executable test suite.
  Every adapter's test package calls `conformance.Run(t, p, script)`; a
  mismatch in either direction (declared-working-but-broken, or
  declared-unsupported-but-quietly-works) fails. `Check` returns per-behavior
  results for programmatic use — the Settings provider preflight renders from
  the same data. Probes the suite cannot run for a given provider (e.g.
  interrupt verification on a PTY-only harness) pass with an explicit
  `Note`, never silently.
- `fake/` — **provider-fake**, the deterministic scripted test double every
  downstream suite runs against: scenario rules (YAML-loadable) for replies,
  chunked streaming, delay on cue, tool events with real file writes, crash
  on cue, and rate-limit on cue; a built-in echo/slow/recall dialect backstops
  unmatched prompts so any scenario'd fake still passes conformance.
- `claudecode/` — the reference adapter: persistent stream-json sessions,
  `claude -p` one-shots, `--resume`, PTY via creack/pty, and on-disk
  transcript normalization (golden-pinned). CI runs the full conformance
  suite against a scripted stand-in subprocess — no claude binary needed;
  `MADS_CLAUDE_SMOKE=1` opts into the real-binary smoke.
- `fastmodel/` — the one-shot job runner every transient job uses (synthesis,
  summaries, retro steps): prompt in, validated JSON out, bounded feedback
  retries. Baseline transport is any provider's OneShot; configuring an
  OpenAI-compatible `BaseURL` flips the transport without caller changes.
  Rate limits bubble as the typed error, never retried.
- `kirocli/` — the second adapter (W7.1): `kiro-cli acp` JSON-RPC for
  interactive sessions, `kiro-cli chat --no-interactive` for one-shots,
  `--resume-id` for reattach, and ACP `session/load` replay for transcript
  normalization. More capable than claude-code on ModelDiscovery, Interrupt,
  and Transcript; honestly Unsupported on CostReporting (Kiro's billing is
  credit-based, not token-based) — the matrix is not a copy of claude-code's.
  Every claim is sourced from public docs, not a live binary (kiro-cli isn't
  installed here); CI runs the full conformance suite against a scripted
  stand-in, `MADS_KIRO_SMOKE=1` opts into the real-binary smoke.
- `acprpc/` — the generic ACP v1 client engine (not a provider): the spec is
  normative, agent adaptation happens through a small enumerated knob set
  where every knob carries a capture or goose-as-client citation, and the
  engine invariants (kind-based permission selection, capability gating,
  own-flag interrupt tracking, fail-closed tool identity) are deliberately
  not knobs. kirocli and goosecli ride it; CI proves it with an adversarial
  scripted fake plus byte-replay of the committed goose captures.
- `goosecli/` — the goose adapter: an acprpc binding (knob values plus the
  trust tool-class table), OneShot emulated over ACP (the `goose run` plan
  superseded — its stream announces no session id), `goose session` PTY
  with banner ref discovery and a SessionLocator backstop, and `session
  export` transcripts. Every claim is capture/source-pinned against goose
  1.48.0 (`goosecli/testdata/captures/`); CI runs the full conformance
  suite against a scripted stand-in, no goose binary needed.
- `codexcli/` — the OpenAI Codex CLI adapter, registry name `codex`
  (shown as "Codex (ChatGPT)"): its own small JSON-RPC client for
  `codex app-server` (threads, turns, items — not ACP), `turn/steer` for a
  mid-turn Send, approvals mapped onto codex's sandbox + approval policy,
  `thread/fork` branching, `thread/compact/start`, `thread/items/list`
  transcripts, and a reaper for the tool processes codex moves out of its
  process group. A per-thread config override stops codex persisting folder
  trust into the user's `~/.codex/config.toml`. Capture-pinned against
  codex 0.154.0 (`codexcli/testdata/captures/`); CI runs the full
  conformance suite against a scripted stand-in, `MADS_CODEX_SMOKE=1` opts
  into the real-binary smoke.

## Test it

```sh
go test -race ./providers/...
```

The conformance suite's own correctness is proven in `conformance/` against
an in-memory stub: an honest stub passes (full and partial matrices, ACP-only
and PTY-only), and every lie class is caught (`TestMismatchesAreCaught`).

## Contract notes

- A `Session` turn: one `Send`, streamed `Event`s, closed by `EventTurnEnd`.
  The events channel closing **without** a turn end is the process-died signal
  the Router's supervision consumes.
- `Interrupt` ends the turn but not the session; conformance proves the
  session serves another turn afterward.
- Resume is `StartOptions.Resume` (a previous `Session.Ref`) — the same spawn
  path as starting, because that is how real harnesses work.
- Rate limits surface as `*provider.RateLimitError` (inform-and-wait, §11.9);
  unsupported behaviors as `provider.ErrUnsupported`.
- Trust vs env_mode: two independent axes governing a spawned agent, not one.
  `StartOptions.Trust` (`provider.Trust`, MADS-UX §3.2) governs what an agent
  can **DO** — which tools it may use, gated per session. `Config.EnvMode`
  (`pkg/agentenv`) governs what an agent can **SEE** — the OS-level
  environment variables visible to the spawned process. A full-trust agent
  in isolated env_mode can use any tool but still can't read a shell secret
  that isn't a bound mads credential or an explicit `env_passthrough` name;
  conversely, an inherit-mode agent sees the whole parent environment
  regardless of trust. Every claudecode/kirocli spawn path (`Start`,
  `OneShot`, PTY) builds its environment through the shared
  `agentenv.Build` helper — see that package's doc comment for the isolated
  base allowlist and passthrough semantics — so inherit/isolated behavior is
  defined exactly once and can't drift between spawn sites (goosecli and
  codexcli build theirs the same way).
