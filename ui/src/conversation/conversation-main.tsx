import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { ConversationDemo } from "./ConversationDemo";
import "../styles/global.css";
import "../tokens/tokens.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ConversationDemo />
  </StrictMode>,
);
