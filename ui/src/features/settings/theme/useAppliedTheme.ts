import { useEffect, useState } from "react";

function applied(): "dark" | "light" {
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

/**
 * The theme applyTheme last put on the document root — "system" already
 * resolved — kept current as it changes, so a scoped preview can match it.
 */
export function useAppliedTheme(): "dark" | "light" {
  const [value, setValue] = useState(applied);
  useEffect(() => {
    const observer = new MutationObserver(() => setValue(applied()));
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  }, []);
  return value;
}
