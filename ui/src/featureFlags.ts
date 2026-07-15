// Real feature-flag surface backed by GET/PUT /config's feature_flags
// (Settings > Advanced). App.tsx calls setFlags(config.feature_flags) once
// on mount, alongside applyTheme; AdvancedPanel calls it again after each
// successful save. Consumers read FLAGS directly (no hook) and can
// subscribeFlags() if they need to re-render on a change.
export interface FeatureFlagState {
  multiRepo: boolean;
  linkedSessions: boolean;
  remoteRegistry: boolean;
}

export const FLAGS: FeatureFlagState = {
  multiRepo: false,
  linkedSessions: false,
  remoteRegistry: false,
};

const listeners = new Set<() => void>();

export function setFlags(next: FeatureFlagState): void {
  Object.assign(FLAGS, next);
  listeners.forEach((listener) => listener());
}

export function subscribeFlags(cb: () => void): () => void {
  listeners.add(cb);
  return () => listeners.delete(cb);
}
