import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { TasksDemo } from "./TasksDemo";
import "../styles/global.css";
import "../tokens/tokens.css";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <TasksDemo />
  </StrictMode>,
);
