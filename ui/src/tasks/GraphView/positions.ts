const key = (sessionId: string): string => `mads.taskGraph.positions.${sessionId}`;

export function loadPositions(sessionId: string): Record<string, { x: number; y: number }> {
  try {
    const raw = localStorage.getItem(key(sessionId));
    return raw ? JSON.parse(raw) : {};
  } catch {
    return {};
  }
}

export function savePositions(sessionId: string, positions: Record<string, { x: number; y: number }>): void {
  localStorage.setItem(key(sessionId), JSON.stringify(positions));
}
