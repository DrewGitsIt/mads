import { useCallback, useRef, useState, type ReactNode } from "react";
import styles from "./ResizablePanel.module.css";

export type ResizeDirection = "horizontal" | "vertical";

export interface ResizablePanelProps {
  children: ReactNode;
  direction?: ResizeDirection;
  defaultSize?: number;
  minSize?: number;
  maxSize?: number;
  className?: string;
  onResizeEnd?: (size: number) => void;
}

export function ResizablePanel({
  children,
  direction = "vertical",
  defaultSize = 300,
  minSize = 120,
  maxSize = 640,
  className,
  onResizeEnd,
}: ResizablePanelProps) {
  const [size, setSize] = useState(defaultSize);
  const sizeRef = useRef(size);
  sizeRef.current = size;
  const dragState = useRef<{ start: number; startSize: number } | null>(null);

  const handleMouseMove = useCallback(
    (e: MouseEvent) => {
      if (!dragState.current) return;
      const delta =
        direction === "vertical"
          ? dragState.current.start - e.clientY
          : e.clientX - dragState.current.start;
      const next = Math.min(maxSize, Math.max(minSize, dragState.current.startSize + delta));
      sizeRef.current = next;
      setSize(next);
    },
    [direction, minSize, maxSize],
  );

  // Reads sizeRef, not `size` — this callback is registered on window at
  // mousedown time and must see the latest dragged value, not the value
  // closed over then.
  const handleMouseUp = useCallback(() => {
    dragState.current = null;
    window.removeEventListener("mousemove", handleMouseMove);
    window.removeEventListener("mouseup", handleMouseUp);
    onResizeEnd?.(sizeRef.current);
  }, [handleMouseMove, onResizeEnd]);

  const handleMouseDown = useCallback(
    (e: React.MouseEvent) => {
      dragState.current = {
        start: direction === "vertical" ? e.clientY : e.clientX,
        startSize: size,
      };
      window.addEventListener("mousemove", handleMouseMove);
      window.addEventListener("mouseup", handleMouseUp);
    },
    [direction, size, handleMouseMove, handleMouseUp],
  );

  const sizeStyle = direction === "vertical" ? { height: size } : { width: size };

  return (
    <div
      className={[styles.panel, styles[direction], className]
        .filter(Boolean)
        .join(" ")}
      style={sizeStyle}
    >
      <div
        className={styles.handle}
        data-testid="resize-handle"
        onMouseDown={handleMouseDown}
      />
      <div className={styles.content}>{children}</div>
    </div>
  );
}
