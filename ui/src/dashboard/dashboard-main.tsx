import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { DashboardDemo } from "./DashboardDemo";
import "../styles/global.css";
import "../tokens/tokens.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <DashboardDemo />
  </StrictMode>,
);
