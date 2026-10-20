import { createContext, useContext } from "react";

/**
 * Full-width mode: the session workspace slides over the sessions sidebar
 * (the icon rail stays). The Changes tab asks for it on small screens, where
 * the diff otherwise gets ~640px (docs/CHANGES-DESIGN.md). SessionsSurface
 * owns the state; whoever asks clears it again when it unmounts or loses
 * the tab. Nothing announces sessions needing attention while the sidebar is
 * covered: the user is reading code, on purpose.
 */
export interface FullWidthApi {
  full: boolean;
  setFull: (on: boolean) => void;
}

export const FullWidthContext = createContext<FullWidthApi | null>(null);

/** Null outside SessionsSurface (the files.html demo, tests): callers hide the control. */
export function useFullWidth(): FullWidthApi | null {
  return useContext(FullWidthContext);
}
