import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { Button } from "../components/Button/Button";
import { Chip } from "../components/Chip/Chip";
import { ConfirmDialog } from "../components/ConfirmDialog/ConfirmDialog";
import { SegmentedControl, type SegmentedOption } from "../components/SegmentedControl/SegmentedControl";
import { Toast, type ToastTone } from "../components/Toast/Toast";
import type { RouterClient } from "../router/client";
import type { ChangeBaselineId, ChangeTurn, ChangedFile, FileDiff } from "../router/types";
import { FileCounts, KindBadge, LineCounts } from "./ChangeBits";
import { buildCommitGroups, buildTierGroups, buildTurnGroups } from "./changeGroups";
import { FolderTreeView, GroupList, type ChangeSelection, type RowContext } from "./ChangesList";
import { filterFiles, sumLines, turnByN, type ChangesFocus } from "./changesModel";
import { DiffViewer } from "./DiffViewer/DiffViewer";
import { buildFolderTree } from "./folderTree";
import { useSeenState } from "./seenState";
import { TurnStrip } from "./TurnStrip";
import { useChangeSet } from "./useChangeSet";
import styles from "./ChangesTab.module.css";

export interface ChangesTabProps {
  sessionId: string;
  routerClient: RouterClient;
  /** A file to select — the Commit dialog's path links. */
  focusPath?: string;
  /** A file in a recorded commit to select — the auto-merged Land card's "See the changes". A fresh object per click. */
  focusCommit?: { hash: string; path: string };
  /** The rail's file click / "Open in Changes": file, tier and turn. A fresh object per click. */
  focusView?: ChangesFocus;
}

export type Grouping = "tier" | "folder" | "turn" | "commit" | "intent";

interface ToastState {
  tone: ToastTone;
  title: string;
  description?: string;
}

/** How long a focus request waits for its row to render (the change set may still be loading). */
const REVEAL_WAIT_MS = 3000;

/** Programmatic scrolls (strip click → list) shouldn't be second-guessed by scroll-spy for this long. */
const SPY_HOLD_MS = 150;

/**
 * The Changes tab (docs/CHANGES-DESIGN.md, docs/mockups/changes-v2.html):
 * a baseline switcher and totals, the turn strip, a grouped file list, and
 * the selected file's Monaco diff. Everything is read from the session's
 * server-computed ChangeSet; the tab only groups, filters and sorts it.
 */
export function ChangesTab({ sessionId, routerClient, focusPath, focusCommit, focusView }: ChangesTabProps) {
  const [vs, setVs] = useState<ChangeBaselineId>("start");
  const { changeSet: cs, loading, error, refetch } = useChangeSet(routerClient, sessionId, vs);
  const { isUnseen, markSeen } = useSeenState(sessionId);

  const [grouping, setGroupingState] = useState<Grouping>("tier");
  const [query, setQuery] = useState("");
  const [turnSel, setTurnSel] = useState<number[] | null>(null);
  const [spy, setSpy] = useState<number | null>(null);
  const [open, setOpen] = useState<Record<string, boolean>>({});
  const [selection, setSelection] = useState<ChangeSelection | null>(null);
  const [confirming, setConfirming] = useState<ChangedFile[] | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [toast, setToast] = useState<ToastState | null>(null);
  const [diff, setDiff] = useState<FileDiff | null>(null);
  const [diffLoading, setDiffLoading] = useState(false);
  const [diffError, setDiffError] = useState<string>();

  const listRef = useRef<HTMLDivElement>(null);
  const filterRef = useRef<HTMLDivElement>(null);
  const [filterH, setFilterH] = useState(41);
  const spyHoldUntil = useRef(0);
  /** A row or section to scroll into view once it renders (focus requests, header links). */
  const [reveal, setRevealState] = useState<{ selector: string; until: number } | null>(null);
  const setReveal = useCallback(
    (r: { selector: string } | null) => setRevealState(r && { ...r, until: Date.now() + REVEAL_WAIT_MS }),
    [],
  );

  const files = useMemo(() => cs?.files ?? [], [cs]);
  const turns = useMemo(() => cs?.turns ?? [], [cs]);
  const git = cs?.git ?? false;
  const filterMode = grouping !== "turn";

  // Commit grouping needs git; Intent needs a plug-in (not this increment).
  const effectiveGrouping: Grouping = grouping === "intent" || (grouping === "commit" && cs && !git) ? "tier" : grouping;

  const setGrouping = useCallback(
    (g: Grouping) => {
      setGroupingState(g);
      setSpy(null);
      if (g === "turn") setTurnSel(null);
      // A selection under a commit shows that commit's diff; outside the
      // Commit grouping there are no commit rows, so fall back to the file.
      setSelection((sel) => {
        if (!sel) return sel;
        if (g !== "commit") return sel.commit ? { path: sel.path } : sel;
        const f = files.find((x) => x.path === sel.path);
        if (!sel.commit && f && !f.uncommitted && f.commits.length) return { path: sel.path, commit: f.commits[0] };
        return sel;
      });
      if (listRef.current) listRef.current.scrollTop = 0;
      setReveal({ selector: '[aria-current="true"]' });
    },
    [files, setReveal],
  );

  const visible = useMemo(
    () => filterFiles(files, { query, turns: filterMode ? turnSel : null }),
    [files, query, filterMode, turnSel],
  );

  const isOpen = useCallback((key: string, dflt: boolean) => open[key] ?? dflt, [open]);
  const toggle = useCallback(
    (key: string, dflt: boolean) => setOpen((o) => ({ ...o, [key]: !(o[key] ?? dflt) })),
    [],
  );

  const select = useCallback((sel: ChangeSelection) => setSelection(sel), []);
  const rowCtx: RowContext = { selection, onSelect: select, isUnseen, isOpen, toggle };

  // --- Focus requests ---------------------------------------------------------

  // A focused file's row must be reachable: once the change set has it,
  // clear a filter that hides it and open its tier section.
  const [exposePath, setExposePath] = useState<string | null>(null);
  useEffect(() => {
    if (!exposePath) return;
    const f = files.find((x) => x.path === exposePath);
    if (!f) return;
    setExposePath(null);
    setQuery((q) => (q && !f.display.toLowerCase().includes(q.trim().toLowerCase()) ? "" : q));
    setTurnSel((t) => (t && !f.turns.some((n) => t.includes(n)) ? null : t));
    setOpen((o) => ({ ...o, [`t:${f.tier}`]: true }));
  }, [exposePath, files]);

  // The Commit dialog's path links.
  useEffect(() => {
    if (!focusPath) return;
    setSelection({ path: focusPath });
    setExposePath(focusPath);
    setGroupingState((g) => (g === "commit" ? "tier" : g));
    setReveal({ selector: `[data-path="${CSS.escape(focusPath)}"]` });
  }, [focusPath, setReveal]);

  // The Land card's "See the changes": Commit grouping, that commit's file.
  useEffect(() => {
    if (!focusCommit) return;
    setGroupingState("commit");
    setTurnSel(null);
    setQuery("");
    setSelection({ path: focusCommit.path, commit: focusCommit.hash });
    setReveal({ selector: `[data-path="${CSS.escape(focusCommit.path)}"][data-commit="${CSS.escape(focusCommit.hash)}"]` });
  }, [focusCommit, setReveal]);

  // The rail: tier into view, the rail's turn as the strip's filter, the file selected.
  useEffect(() => {
    if (!focusView) return;
    setGroupingState((g) => (g === "turn" || g === "commit" ? "tier" : g));
    setSpy(null);
    if (focusView.turn !== undefined) setTurnSel(focusView.turn === null ? null : [focusView.turn]);
    if (focusView.tier && focusView.tier !== "all") {
      const tier = focusView.tier;
      setOpen((o) => ({ ...o, [`t:${tier}`]: true }));
    }
    if (focusView.path) {
      setSelection({ path: focusView.path });
      setExposePath(focusView.path);
      setReveal({ selector: `[data-path="${CSS.escape(focusView.path)}"]` });
    } else if (focusView.tier && focusView.tier !== "all") {
      setReveal({ selector: `[data-group-key="t:${focusView.tier}"]` });
    }
  }, [focusView, setReveal]);

  useLayoutEffect(() => {
    if (!reveal) return;
    const el = listRef.current?.querySelector<HTMLElement>(reveal.selector);
    if (el) el.scrollIntoView?.({ block: "nearest" });
    if (el || Date.now() > reveal.until) setRevealState(null);
    // Re-checked whenever what the list renders changes, until the row shows up.
  }, [reveal, cs, open, turnSel, query, grouping, selection]);

  // --- Selected file and its diff ---------------------------------------------

  const selectedFile = selection ? files.find((f) => f.path === selection.path) : undefined;
  // A working selection whose file left the set (put back, deleted, or a
  // different baseline): say so instead of fetching a diff that 404s.
  const gone = !!selection && !selection.commit && !!cs && !selectedFile;
  const haveSet = cs !== null;
  const diffVersion = selectedFile ? `${selectedFile.last_changed}|${selectedFile.added}|${selectedFile.deleted}` : "";

  useEffect(() => {
    if (selectedFile) markSeen(selectedFile);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- by path + last_changed
  }, [selectedFile?.path, selectedFile?.last_changed, markSeen]);

  useEffect(() => {
    if (!selection || gone || (!selection.commit && !haveSet)) {
      setDiff(null);
      setDiffError(undefined);
      setDiffLoading(false);
      return;
    }
    let cancelled = false;
    setDiffLoading(true);
    setDiffError(undefined);
    const request = selection.commit
      ? routerClient.getCommitFileDiff(sessionId, selection.commit, selection.path)
      : routerClient.getFileDiff(sessionId, selection.path, undefined, vs);
    request
      .then((d) => {
        if (!cancelled) setDiff(d);
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setDiff(null);
          setDiffError(err instanceof Error ? err.message : String(err));
        }
      })
      .finally(() => {
        if (!cancelled) setDiffLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- keyed on the selection's fields, not its identity
  }, [routerClient, sessionId, selection?.path, selection?.commit, vs, gone, diffVersion, haveSet]);

  // --- Turn grouping: scroll-spy and navigation -----------------------------

  // Group headers stick just under the filter bar; it wraps at narrow widths.
  useLayoutEffect(() => {
    const el = filterRef.current;
    if (!el) return;
    const measure = () => {
      if (el.offsetHeight) setFilterH(el.offsetHeight);
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [haveSet]);

  const computeSpy = useCallback(() => {
    const host = listRef.current;
    if (!host) return;
    const y = host.scrollTop + filterH + 4;
    let cur: number | null = null;
    host.querySelectorAll<HTMLElement>("[data-turn]").forEach((el) => {
      if (cur === null || el.offsetTop <= y) cur = Number(el.dataset.turn);
    });
    setSpy(cur);
  }, [filterH]);

  useEffect(() => {
    if (effectiveGrouping === "turn") computeSpy();
  }, [effectiveGrouping, computeSpy, turns.length]);

  function onListScroll() {
    if (effectiveGrouping !== "turn" || Date.now() < spyHoldUntil.current) return;
    computeSpy();
  }

  function scrollToTurn(n: number) {
    const host = listRef.current;
    const el = host?.querySelector<HTMLElement>(`[data-turn="${n}"]`);
    if (host && el) {
      spyHoldUntil.current = Date.now() + SPY_HOLD_MS;
      host.scrollTop = el.offsetTop - filterH;
    }
    setSpy(n);
  }

  // --- Delete scratch ---------------------------------------------------------

  async function confirmDelete() {
    const targets = confirming ?? [];
    setConfirming(null);
    setDeleting(true);
    try {
      const res = await routerClient.deleteScratch(
        sessionId,
        targets.map((f) => f.path),
      );
      const n = (res.deleted ?? []).length;
      const skipped = res.skipped ?? [];
      setToast({
        tone: skipped.length ? "warn" : "ok",
        title: `Deleted ${n} scratch ${n === 1 ? "file" : "files"}${skipped.length ? `, skipped ${skipped.length}` : ""}`,
        description: skipped.length
          ? skipped.map((s) => `${displayOf(s.path)}: ${s.reason}`).join("\n")
          : undefined,
      });
    } catch (err) {
      setToast({ tone: "bad", title: "Couldn't delete scratch files", description: err instanceof Error ? err.message : String(err) });
    } finally {
      setDeleting(false);
      refetch();
    }
  }

  function displayOf(path: string): string {
    return files.find((f) => f.path === path)?.display ?? path;
  }

  // Success toasts go away by themselves; skips and failures wait to be read.
  useEffect(() => {
    if (!toast || toast.tone !== "ok") return;
    const t = setTimeout(() => setToast(null), 6000);
    return () => clearTimeout(t);
  }, [toast]);

  // --- Render -----------------------------------------------------------------

  if (!cs) {
    return (
      <div className={styles.tab}>
        <div className={styles.status}>
          {loading || !error ? (
            "Loading changes…"
          ) : (
            <>
              <span>Couldn't load changes: {error}</span>
              <Button variant="secondary" size="sm" onClick={refetch}>
                Retry
              </Button>
            </>
          )}
        </div>
      </div>
    );
  }

  const baselines = cs.baselines.filter((b) => b.available);
  const filtered = visible.length !== files.length;
  const shownTotals = filtered ? sumLines(visible) : { added: cs.totals.added, deleted: cs.totals.deleted };

  const groupOptions: SegmentedOption<Grouping>[] = [
    { value: "tier", label: "Tier" },
    { value: "folder", label: "Folder" },
    { value: "turn", label: "Turn", disabled: turns.length === 0, title: turns.length ? undefined : "No turns recorded" },
    { value: "commit", label: "Commit", disabled: !git, title: git ? undefined : "Not a git repository" },
    { value: "intent", label: "Intent", disabled: true, title: "Needs a grouping plug-in" },
  ];

  let body: ReactNode;
  if (files.length === 0) {
    body = <div className={styles.empty}>No changes yet</div>;
  } else if (visible.length === 0 && effectiveGrouping !== "turn") {
    body = <div className={styles.empty}>No files match.</div>;
  } else if (effectiveGrouping === "folder") {
    body = <FolderTreeView tree={buildFolderTree(visible)} ctx={rowCtx} />;
  } else if (effectiveGrouping === "turn") {
    body = <GroupList groups={buildTurnGroups(visible, turns)} ctx={rowCtx} />;
  } else if (effectiveGrouping === "commit") {
    body = <GroupList groups={buildCommitGroups(visible, cs.commits)} ctx={rowCtx} />;
  } else {
    body = <GroupList groups={buildTierGroups(visible)} ctx={rowCtx} onDeleteScratch={(fs) => setConfirming(fs)} />;
  }

  return (
    <div className={styles.tab}>
      <div className={styles.toolbar}>
        {baselines.length > 1 && (
          <>
            <span className={styles.lbl} id={`changes-vs-${sessionId}`}>
              Compare to
            </span>
            <SegmentedControl
              aria-labelledby={`changes-vs-${sessionId}`}
              value={vs}
              options={baselines.map((b) => ({ value: b.id, label: b.label }))}
              onChange={setVs}
            />
          </>
        )}
        <span className={styles.totals} data-testid="changes-totals">
          {filtered ? `${visible.length} of ${files.length}` : files.length} {files.length === 1 ? "file" : "files"} ·{" "}
          <LineCounts added={shownTotals.added} deleted={shownTotals.deleted} />
        </span>
        {loading && <span className={styles.quiet}>Updating…</span>}
        {error && <span className={styles.quiet}>Couldn't refresh: {error}</span>}
        {cs.notes && cs.notes.length > 0 && <span className={styles.notes}>{cs.notes.join(" · ")}</span>}
      </div>

      {turns.length > 0 && (
        <TurnStrip
          turns={turns}
          mode={effectiveGrouping === "turn" ? "navigate" : "filter"}
          selected={turnSel}
          onSelect={setTurnSel}
          spy={spy}
          onNavigate={scrollToTurn}
        />
      )}

      <div className={styles.body}>
        <div
          className={styles.list}
          ref={listRef}
          onScroll={onListScroll}
          style={{ "--filter-h": `${filterH}px` } as CSSProperties}
          data-testid="changes-list"
        >
          <div className={styles.filter} ref={filterRef}>
            <input
              className={styles.input}
              type="search"
              placeholder="Filter files…"
              aria-label="Filter files"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
            <SegmentedControl
              aria-label="Group by"
              className={styles.groupBy}
              value={effectiveGrouping}
              options={groupOptions}
              onChange={setGrouping}
            />
          </div>
          {body}
        </div>

        <div className={styles.detail}>
          <DiffViewer
            path={selection?.path}
            commitHash={selection?.commit}
            diff={diff}
            loading={diffLoading}
            error={diffError}
            emptyMessage={
              gone
                ? "This file no longer differs from the baseline: it was put back or deleted."
                : files.length === 0
                  ? "No changes yet."
                  : "Select a file to see its diff."
            }
            onOpenInEditor={(p) => void routerClient.openInEditor(p)}
            header={
              selection ? (
                <DetailHeader
                  file={selectedFile}
                  path={selection.path}
                  turns={turns}
                  onCommitLink={(hash) => {
                    setGroupingState("commit");
                    setTurnSel(null);
                    setSelection({ path: selection.path, commit: hash });
                    setReveal({ selector: `[data-path="${CSS.escape(selection.path)}"][data-commit="${CSS.escape(hash)}"]` });
                  }}
                  onTurnChip={(n) => {
                    if (effectiveGrouping === "turn") setGroupingState("tier");
                    setSpy(null);
                    setTurnSel([n]);
                    setSelection({ path: selection.path });
                  }}
                />
              ) : undefined
            }
          />
        </div>
      </div>

      <ConfirmDialog
        open={confirming !== null}
        title={`Delete ${confirming?.length ?? 0} scratch ${confirming?.length === 1 ? "file" : "files"}?`}
        description={
          <>
            <p>
              These files were created during this session and match the scratch rules. They&apos;ll be deleted from
              disk. This can&apos;t be undone.
            </p>
            <ul className={styles.confirmList}>
              {(confirming ?? []).map((f) => (
                <li key={f.path}>
                  {f.display}
                  {f.scratch_rule && <span className={styles.rule}> · {f.scratch_rule}</span>}
                </li>
              ))}
            </ul>
          </>
        }
        confirmLabel="Delete files"
        confirmDisabled={deleting}
        onConfirm={() => void confirmDelete()}
        onCancel={() => setConfirming(null)}
      />

      {toast && (
        <div className={styles.toast}>
          <Toast tone={toast.tone} title={toast.title} description={toast.description} onDismiss={() => setToast(null)} />
        </div>
      )}
    </div>
  );
}

function DetailHeader({
  file,
  path,
  turns,
  onCommitLink,
  onTurnChip,
}: {
  file: ChangedFile | undefined;
  path: string;
  turns: ChangeTurn[];
  onCommitLink: (hash: string) => void;
  onTurnChip: (n: number) => void;
}) {
  if (!file) {
    return (
      <span className={styles.dpath} title={path}>
        <span className={styles.dpathText}>{path}</span>
      </span>
    );
  }
  return (
    <>
      <KindBadge change={file.change} />
      <span className={styles.dpath} title={file.path}>
        <span className={styles.dpathText}>{file.display}</span>
      </span>
      <FileCounts file={file} />
      {file.commits.length > 0 && (
        <Chip tone="neutral">
          committed in{" "}
          <button type="button" className={styles.link} onClick={() => onCommitLink(file.commits[0])}>
            {file.commits[0].slice(0, 7)}
          </button>
        </Chip>
      )}
      {(file.uncommitted || file.commits.length === 0) && <Chip tone="neutral">uncommitted</Chip>}
      {file.turns.length > 0 && (
        <span className={styles.changedIn}>
          <span className={styles.lbl}>changed in</span>
          {file.turns.map((n) => (
            <span key={n} title={turnByN(turns, n)?.prompt || `Turn ${n}`}>
              <Chip tone="neutral" className={styles.tchip} onClick={() => onTurnChip(n)}>
                turn {n}
              </Chip>
            </span>
          ))}
        </span>
      )}
    </>
  );
}
