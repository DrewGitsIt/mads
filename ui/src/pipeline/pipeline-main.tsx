import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { PipelineDemo } from "./PipelineDemo";
import "../styles/global.css";
import "../tokens/tokens.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <PipelineDemo />
  </StrictMode>,
);
