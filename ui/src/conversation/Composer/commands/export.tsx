import { ExportCard } from "./ExportCard";
import type { CommandApi, CommandDescriptor } from "./types";

type Format = "md" | "json";

/** Saves a file via the router's attachment URL — works in the browser and Electron. */
function download(url: string) {
  const a = document.createElement("a");
  a.href = url;
  a.download = "";
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
}

function start(api: CommandApi, format: Format) {
  download(api.ctx.routerClient.exportSessionUrl(api.ctx.sessionId, format));
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
      start(api, arg);
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
      start(api, saved);
      return;
    }
    api.openCard((close) => (
      <ExportCard
        onDownload={async (format, remember) => {
          close();
          start(api, format);
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
