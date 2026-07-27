/**
 * `Session.summary` is three-section markdown produced by pkg/sessionsummary:
 * "## What was accomplished\n\n<body>\n\n## Key decisions\n\n…". The rail wants
 * a one-line teaser of the *accomplished* narrative, not the literal "##"
 * heading — rendering the raw markdown into a nowrap/ellipsis clamp showed the
 * scaffolding ("## What was accomplished …") instead of the content. This
 * pulls the first section's body as plain text; the full markdown still renders
 * in SessionSummaryCard when the session is opened.
 */
export function summaryTeaser(summary: string): string {
  const body: string[] = [];
  for (const line of summary.split("\n")) {
    const trimmed = line.trim();
    if (trimmed === "") {
      // Blank line ends the first paragraph once we have content.
      if (body.length) break;
      continue;
    }
    if (trimmed.startsWith("#")) {
      // A heading after we've collected body means the next section started.
      if (body.length) break;
      continue;
    }
    body.push(trimmed);
  }
  // Strip inline markdown emphasis/code so the clamp reads as prose.
  return body.join(" ").replace(/[*_`]/g, "");
}
