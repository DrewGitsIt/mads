import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { guardStrayFileDrops } from "./components/Attachments/guardStrayFileDrops";
import "./styles/global.css";
import "./tokens/tokens.css";

guardStrayFileDrops();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
