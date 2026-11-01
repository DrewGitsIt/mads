import { ExportCard } from "./ExportCard";
import type { CommandApi, CommandDescriptor } from "./types";

type Format = "md" | "json";

/** The name in `Content-Disposition: attachment; filename="…"` (Go's %q quoting). */
export function dispositionFilename(header: string | null): string {
  const m = header?.match(/filename="((?:[^"\\]|\\.)*)"/);
  return m ? m[1].replace(/\\(.)/g, "$1") : "";
}

/**
 * Fetches the export and saves it from a blob. Not a plain <a download>
 * pointing at the Router: that's a navigation, which sends no Origin, and
 * the Router refuses an Origin-less request from another localhost page —
 * so from the UI dev server the download never arrived. A fetch carries
 * Origin like every other API call. Works in the browser and Electron.
 */
async function download(api: CommandApi, url: string) {
  let blob: Blob;
  let name: string;
  try {
    const res = await fetch(url);
    if (!res.ok) throw new Error(`the Router answered ${res.status}`);
    name = dispositionFilename(res.headers.get("Content-Disposition"));
    blob = await res.blob();
  } catch (err) {
    api.fail(`Couldn't export this conversation: ${err instanceof Error ? err.message : String(err)}`);
    return;
  }
  const href = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = href;
  a.download = name;
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
  // The click has started the save; the blob URL isn't needed after it.
  setTimeout(() => URL.revokeObjectURL(href), 0);
}

function start(api: CommandApi, format: Format): Promise<void> {
  return download(api, api.ctx.routerClient.exportSessionUrl(api.ctx.sessionId, format));
}

/**
 * /export [md|json] — downloads the whole conversation. With a saved format
 * (Config.export_format) it goes straight away; an argument overrides it for
 * one export. Otherwise a card asks, and "Always use this format" saves it.
 */
export const exportCommand: CommandDescriptor = {
  name: "export",
  description: "Download this conversation",
  argHint: "[md|json]",
  execute: async ({ args }, api) => {
    const arg = args.trim().toLowerCase();
    if (arg) {
      if (arg !== "md" && arg !== "json") {
        api.fail(`Unknown export format "${args.trim()}" (use md or json)`);
        return;
      }
      await start(api, arg);
      return;
    }
    const { routerClient } = api.ctx;
    let saved = "";
    try {
      saved = (await routerClient.getConfig()).export_format ?? "";
    } catch {
      // Can't read the preference: fall through to asking.
    }
    if (saved === "md" || saved === "json") {
      await start(api, saved);
      return;
    }
    api.openCard((close) => (
      <ExportCard
        onDownload={async (format, remember) => {
          close();
          void start(api, format);
          if (!remember) return;
          try {
            // PUT /config replaces the row, so read-modify-write.
            const cfg = await routerClient.getConfig();
            await routerClient.updateConfig({ ...cfg, export_format: format });
          } catch (err) {
            api.fail(
              `Couldn't save the export format: ${err instanceof Error ? err.message : String(err)}`,
            );
          }
        }}
      />
    ));
  },
};
