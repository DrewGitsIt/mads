# Goose Hooks Gap — research report

**TL;DR: The premise is incorrect.** Current goose **does** ship a full lifecycle-hooks framework (`PreToolUse`, `PostToolUse`, `Stop`, `SessionStart/End`, `UserPromptSubmit`, file/shell events) implementing the **Open Plugins** hooks spec — the same shape as Claude Code's hooks and the same exit-2 / `{"decision":"block"}` deny protocol. The real situation is a **version/surface gap in your mads build**, not a design choice by goose.

---

## 1. Are there goose issues asking for a hooks framework?

**Verdict: yes, many — and they were granted, not rejected.**

- **PR #9304 `feat(hooks): PreToolUse denial`** — merged **2026-05-19**. This is the hook that lets a plugin veto a tool call. It is the direct equivalent of the claude-code PreToolUse hook you build on, and it is *shipped*.
- **PR #11449 `feat(hooks): add on_failure block for PreToolUse hooks`** — closed/merged **2026-08-25**. Makes a failing hook fail-**closed** (deny the call) instead of silently allowing — exactly the "don't let a broken hook become an open door" guarantee you'd want for `allowed_paths`.
- **Issue #10866 `Configurable fail-closed for blocking PreToolUse command hooks`** — closed.
- **Issue #11399** (open, 12 comments) — *not* a hooks ask. It's "Fine-grained shell permissions: regex-based allow/ask/deny rules for commands under the developer shell tool," i.e. asking for *per-argument* shell rules that the coarse `Always/Ask/Never Allow` tool-level grant can't express.
- **Issue #1065 `Restrict access to files or paths`** — **closed as DONE**; resolved via `.gooseignore` (doc link in a `meenalc` comment: "Closing this since it is done… /guides/using-gooseignore/"). So the path-restriction request was *accepted and shipped*, not deferred.
- **Issue #11272 RFC: fail-closed post-approval authorization seam before tool dispatch** — closed; explicitly treats "the blocking `PreToolUse` hook provides a useful external veto point" as *given*.

There is no "why doesn't goose have hooks" issue, and no evidence hooks were requested *and refused*. The request was made and implemented.

## 2. Does goose's own docs mention hooks / tool-permission / sandboxing?

**Verdict: extensively — there is a dedicated docs section and a launch blog post.**

- **`documentation/docs/guides/context-engineering/hooks.md`** — full "Hooks" guide (566 lines). States: "goose follows the Open Plugins hooks specification." Lists `PreToolUse`, `PostToolUse`, `Stop`, `SessionStart`, `SessionEnd`, `UserPromptSubmit`, `BeforeReadFile`, `AfterFileEdit`, `BeforeShellExecution`, `AfterShellExecution`, and `PreToolUseResult`.
- **`documentation/docs/guides/context-engineering/hooks.md:242–323`** — "Blocking a Tool Call" section, verbatim: *"Most events are observation-only… Two events are different—**`PreToolUse` and `Stop` can block**,"* with the decision protocol (exit `2`, or `{"decision":"block"}` on stdout) and the `on_failure: block` fail-closed mode. **Line 127**: `on_failure: block` "denies it."
- **`documentation/blog/2026-05-14-goose-hooks/index.md`** — launch post: *"goose now supports lifecycle hooks… Wire shell scripts into PreToolUse, PostToolUse, … If you've used Claude Code's hooks… it's the same idea."*
- **`documentation/docs/guides/managing-tools/goose-permissions.md`** + **`tool-permissions.md`** — the permission-mode docs (Autonomous / Manual / Smart / Chat; per-tool Always/Ask/Never Allow) — the ACP surface you already know.
- **`examples/plugins/hello-hooks/`** — in-repo sample plugin with `hooks/hooks.json` + `scripts/announce.sh`, runnable end-to-end.
- **Code**: `crates/goose/src/hooks/mod.rs`, `crates/goose/src/agents/state_machine/ops_entry_hook.rs`, `ops_stop_hook.rs`, `tests/hooks_lifecycle.rs`.

## 3. If goose *does* have a permission/sandbox feature, what's its actual scope?

**Verdict: goose has both a pre-tool veto (hooks) and a static ignore list (`.gooseignore`) — but they are not the same thing you build for claude-code.**

- **(a) Can a goose PRE-tool hook deny a specific call by inspecting arguments (e.g. `file_path` of write/edit)?** **Yes.** From `hooks.md:242–253`: a `PreToolUse` hook receives a JSON payload including `tool_input` (the blog example shows `tool_input` with `command`; the file-tool input carries `file_path`) and `working_dir`; exit `2` (reason from stderr) or `{"decision":"block","reason":…}` **blocks the call and returns a message to the model**. The `matcher` is a **regex over the tool name *or file path* or shell command** (`hooks.md` matcher field), so a hook that inspects `file_path` and exits 2 for out-of-`allowed_paths` is directly expressible. This is precisely the same mechanism your `claudecode/permission.go` PreToolUse hook uses.
- **(b) Does goose read a config at spawn and apply path restrictions to write/edit/shell?** **Partially — read-side only.** `.gooseignore` exists and is honored for **file reads** (the maintainer in #1065: "ensure it is skipped in file reads *at a minimum*"). It is **not** a general allowlist gate on *writes*; it is an ignore list. There is no documented `goose.yaml`/`config.yaml` block that says "only these `allowed_paths` may be written." Path *scope* on writes is a hook job (a `PreToolUse` matcher on `file_path`), today.
- **(c) Does the `developer` builtin apply path restrictions?** **No baked-in path fence.** `developer`'s file/shell tools inherit the global tool-permission level + any PreToolUse hook. The #11399 issue shows `developer__shell` granularity is still a *requested* (open) enhancement — the built-in doesn't enforce it itself.

Net: goose's "permission" surface for **your use case** = tool-level grant (ACP) **+** an arbitrary pre-tool veto (hooks) **+** a read-only ignore list (`.gooseignore`). The per-argument `file_path` write gate that is native to claude-code is, in goose, the **PreToolUse hook path** — which is available but **not the built-in**.

## 4. Is there a DELIBERATE design decision documented for why goose has no hooks?

**Verdict: there is none, because goose doesn't lack hooks — but a documented design *rationale* for how hooks are scoped does exist.**

- The hooks guide's "Blocking a Tool Call" section (hooks.md:242–323) documents the *intentional* design: only `PreToolUse` and `Stop` may block; every other event is observation-only so that "a broken hook can never keep a finished turn from ending" and a mis-allow is never accidental (`on_failure` **defaults to `allow`**, line 127). That is a deliberate, documented fail-open *default* (tightenable to fail-closed per-hook).
- **Issue #11272 (closed RFC)** explicitly reasons about the *residual* gap and why hooks alone are not a "bounded authorization": it wants a **post-approval** seam so an executor can verify the allow at the OS effect point. Maintainers treat the PreToolUse hook as the *veto* and keep authorization as a separate concern.
- The "why block only these two events" is framed as a safety contract (fail-open default, capped `Stop` blocks via `GOOSE_STOP_HOOK_BLOCK_CAP`), not an oversight.

## 5. What do claude-code / Copilot CLI / Aider offer that goose does NOT?

**Verdict: almost nothing at the *hooks* level — goose matches the deny-by-argument mechanism; the gaps are convenience, not capability.**

- **claude-code PreToolUse**: same event, same "inspect tool input, exit non-zero/JSON to deny." goose's is spec-compatible (both follow **Open Plugins** hooks — "goose follows the Open Plugins hooks specification," hooks.md line 16). **Parity, not a gap.**
- **Built-in path allow/deny as first-class config (settings allow/deny lists) rather than a user-written script** — this is where goose is one step behind: you must *write the hook yourself*; claude-code lets you declare the policy in settings. That is the practical delta, and it matches exactly what your `claudecode` provider already does for you (it *generates* that hook).
- **Copilot CLI / Aider**: Aider does its *own* path fence natively (only lets the model edit repo files it knows); Copilot CLI exposes a settings-based allow/deny. goose offers the read-side `.gooseignore` plus the hook escape hatch; it ships **no** native "only these dirs are writable" declarative setting. That single point is the real feature delta.
- **Default posture**: goose PreToolUse hooks **fail open by default** (`on_failure: allow`, hooks.md:127, 286–317) unless you set `on_failure: block`. For an `allowed_paths` boundary you *must* set fail-closed or a hook that times out/crashes silently re-allows.

## 6. Most likely reason goose has "no hooks" — pick one.

**Verdict: (c) the premise is false — goose *has* hooks; the observed gap is (b) a *mads-side wiring/version* gap, and (d) a default-policy posture (fail-open unless configured).**

- **Evidence 1 — it exists in current goose.** PR #9304 (PreToolUse denial) merged 2026-05-19; on_failure fail-closed in #11449 (merged 2026-08-25); `hooks.md` and the 2026-05-14 blog post are in-repo on `main`. It is "shipped, documented, and in-repo as an example" (`examples/plugins/hello-hooks/`).
- **Evidence 2 — your mads pins a pre-hooks version.** `pkg/preflight/compat.go:33`: `"goose": {Min: "1.46.0", Max: "1.49.0", Note: "proven against goose 1.46.0…"}`. Hooks landed in the **#9304** timeline (~May 2026), which is *after* your 1.46–1.49 window, so your mads build simply predates the feature. That is the entire "goose has no hooks" observation.
- **Evidence 3 — mads doesn't wire it either way.** No mads Go code references `hooks.json` or a goose PreToolUse hook (grep over `providers/ pkg/ cmd/` returns only your `claudecode` provider's hook), so even against a hooks-capable goose, `allowed_paths` is not expressed for goose.
- **Evidence 4 — the "won't-do" list.** #1065 (path restriction) was *closed as done* via `.gooseignore`; there is no issue showing goose refused a hook feature and there is no design doc saying "we will not do hooks."

**Bottom line for (a)/(b)/(c):** (a) file a goose issue is the *wrong* move — goose already has the mechanism. Instead **target a goose release with hooks + set `on_failure: block` + emit an Open-Plugins `PreToolUse` hook that inspects `file_path`** (the pattern your `claudecode` provider already emits, re-pointed at goose plugins). If you can't depend on a hooks-capable goose, keep the best-effort UI note: *goose enforces read-side `.gooseignore` and tool-level grants; per-path write scoping needs a `PreToolUse` hook.* Filing an issue vs goose is not supported by the evidence.

---

*Sources (all in-repo on `aaif-goose/goose` `main`): `documentation/docs/guides/context-engineering/hooks.md`; `documentation/blog/2026-05-14-goose-hooks/index.md`; docs `goose-permissions.md`/`tool-permissions.md`; `examples/plugins/hello-hooks/`; `crates/goose/src/hooks/mod.rs`; PRs #9304, #11449, #11120, #10866; issues #1065 (closed), #11399 (open), #11272 (closed RFC).*
