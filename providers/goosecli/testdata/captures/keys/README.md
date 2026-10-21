# goose 1.50.1 — conversation-key captures (survey 2026-10-20)

Evidence for the stable keys (docs/CHAT-STREAM-DESIGN.md §1): goose's live
ACP ids against the rows its own store writes for the same session.

- **Binary / model**: goose 1.50.1 (`/Applications/Goose.app/.../goose acp
  --with-builtin developer`), ollama `qwen3.8-coder-nothink` (Spark).
- **Isolation**: `GOOSE_PATH_ROOT` pointed at a `/tmp` scratch root, so the
  real store (`~/.local/share/goose/sessions/sessions.db`) was never read
  or written by the capture.
- **Scrubbed**: the scratch paths `/tmp/mads-provider-survey/goose/...`
  were rewritten to `/tmp/mads-goose-survey/...`; nothing else changed.

## Files

- `run.wire.jsonl` — the client's stdio log (`dir` c2s / s2c, plus `meta`
  turn markers), five turns: a plain reply (`chatcmpl-904`), text → `shell
  ls` → text (`chatcmpl-103`, `call_6464carb`, `chatcmpl-928`), a markdown
  reply whose chunks span four `created` seconds (`chatcmpl-17`,
  1792515372–375), a count cancelled after its first chunk
  (`chatcmpl-287`, stored partial "1"), and a reply after the cancel
  (`chatcmpl-925`). Trimmed after turn e (the survey went on to SIGKILL
  goose mid-reply).
- `run.store.jsonl` — every `messages` row goose inserted into its
  `sessions.db` during those turns, polled every 25 ms (`t` = when the
  poll saw it): user rows, the hidden `<turn-context>` row after each
  prompt (`metadata.turnContext`, `userVisible:false`), assistant text rows
  under the live `messageId` with the first chunk's `created`, and the tool
  call / tool response as rows of their own (`msg_<uuid>` ids), inserted
  only once the tool had finished (rows 6–8 together).
- `main-store-msgid-collisions.txt` — counts only (no content) from the
  owner's real store, read-only: 27 (session, message_id) pairs repeat,
  up to 3 messages per id, and 780 assistant ids are ollama's
  `chatcmpl-<0..999>`.

The schema these rows live in is `../../sessions-schema.sql` (goose's own
`.schema`, `schema_version` 16).
