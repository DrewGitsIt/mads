// Preloaded into every vitest forks-pool worker (--require, see
// vitest.config.ts) so it is in place before the worker loads anything else.
//
// The workers outlive the main vitest process when only it is killed (an
// agent's command timeout, a script's `kill $pid` — anything short of a
// process-group signal like Ctrl-C). The orphans keep running at full CPU and
// grew to ~4.5 GB each before anyone noticed. Each worker holds an IPC channel
// to the main process, and the OS closes it however the main process dies, so
// die the moment it disconnects. SIGKILL rather than process.exit: vitest
// replaces process.exit in workers with a function that throws.
if (process.connected) {
  process.once("disconnect", () => process.kill(process.pid, "SIGKILL"));
}
