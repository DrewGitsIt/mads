import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { LibrarySurface } from "./LibrarySurface";
import "../styles/global.css";
import "../tokens/tokens.css";

/**
 * Standalone review mount, mirroring tasks.html/files.html's convention.
 * Idiom (b) (router/client.ts's free functions) has no fake-transport
 * equivalent to idiom (a)'s FakeRouterClient, so this talks to a real
 * Router at VITE_ROUTER_URL (default localhost:17373) rather than seeding
 * fixtures — same as the real shell's main.tsx.
 */
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <LibrarySurface />
  </StrictMode>,
);
