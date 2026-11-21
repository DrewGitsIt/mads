import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ConversationDemo } from "./ConversationDemo";
import { resetEventsBusForTests } from "../router/events";
import "../styles/global.css";
import "../tokens/tokens.css";

// The demo runs against FakeRouterClient, but its hooks use the real event
// bus, whose first connect reads GET /events/head — which no demo server
// answers, so the ready barrier never opened and nothing loaded. Prime the
// cursor at 0 as the tests do; the production entry (main.tsx) never does.
resetEventsBusForTests(0);

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ConversationDemo />
  </StrictMode>,
);
