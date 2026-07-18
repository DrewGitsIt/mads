const key = "mads.pipeline.drillInHeight";

export function loadDrillInHeight(): number | undefined {
  try {
    const raw = localStorage.getItem(key);
    return raw ? JSON.parse(raw) : undefined;
  } catch {
    return undefined;
  }
}

export function saveDrillInHeight(px: number): void {
  localStorage.setItem(key, JSON.stringify(px));
}
