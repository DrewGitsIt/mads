import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { FilesDemo } from "./FilesDemo";
import "../styles/global.css";
import "../tokens/tokens.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <FilesDemo />
  </StrictMode>,
);
