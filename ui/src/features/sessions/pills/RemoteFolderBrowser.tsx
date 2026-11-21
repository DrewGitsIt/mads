import { useEffect, useState } from "react";
import { peerBase } from "../../../router/mesh";
import pillStyles from "./pills.module.css";

/** How long one listing may take before it reads as failed. */
export const REMOTE_LIST_TIMEOUT_MS = 10_000;

/** The peer Mesh Service's GET /mesh/fs/list (services/mesh/internal/server/fs.go). */
interface FSListing {
  path: string;
  parent?: string;
  entries: { name: string; path: string }[];
}

/**
 * Folder browsing on another device (docs/MESH-MODE-DESIGN.md D6): the
 * native dialog would show this machine's disk. Directories only, under the
 * peer user's home — the peer's Mesh Service enforces that, this only shows
 * what it answers.
 */
export function RemoteFolderBrowser({ peer, onPick }: { peer: string; onPick: (path: string) => void }) {
  const [listing, setListing] = useState<FSListing>();
  const [path, setPath] = useState<string>("");
  const [error, setError] = useState<string>();

  useEffect(() => {
    let cancelled = false;
    const q = path ? `?path=${encodeURIComponent(path)}` : "";
    fetch(`${peerBase(peer)}/mesh/fs/list${q}`, { signal: AbortSignal.timeout(REMOTE_LIST_TIMEOUT_MS) })
      .then(async (res) => {
        const body = (await res.json()) as FSListing & { error?: string };
        if (!res.ok) throw new Error(body.error || `status ${res.status}`);
        return body;
      })
      .then(
        (l) => {
          if (cancelled) return;
          setListing(l);
          setError(undefined);
        },
        (err: unknown) => {
          if (cancelled) return;
          if (err instanceof DOMException && err.name === "TimeoutError") {
            setError(`${peer} didn't answer in ${REMOTE_LIST_TIMEOUT_MS / 1000} s`);
            return;
          }
          setError(err instanceof Error ? err.message : String(err));
        },
      );
    return () => {
      cancelled = true;
    };
  }, [peer, path]);

  return (
    <div className={pillStyles.remoteBrowser} data-remote-folders={peer}>
      <div className={pillStyles.groupLabel}>Folders on {peer}</div>
      {error && <div className={pillStyles.remoteError}>{error}</div>}
      {listing && (
        <>
          <div className={pillStyles.remotePath} title={listing.path}>
            {listing.path}
          </div>
          <div className={pillStyles.remoteRow}>
            {listing.parent && (
              <button type="button" className={pillStyles.option} onClick={() => setPath(listing.parent!)}>
                ← Up
              </button>
            )}
            <button
              type="button"
              className={[pillStyles.option, pillStyles.optionSelected].join(" ")}
              onClick={() => onPick(listing.path)}
            >
              Use this folder
            </button>
          </div>
          <div className={pillStyles.remoteList}>
            {listing.entries.map((e) => (
              <button key={e.path} type="button" className={pillStyles.option} title={e.path} onClick={() => setPath(e.path)}>
                <span className={pillStyles.optionLabel}>{e.name}/</span>
              </button>
            ))}
            {listing.entries.length === 0 && <div className={pillStyles.remoteError}>No folders here.</div>}
          </div>
        </>
      )}
    </div>
  );
}
