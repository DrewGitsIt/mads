import { useCallback, useRef, useState, type WheelEvent as ReactWheelEvent } from "react";
import type { Task } from "../../router/types";
import { Button } from "../../components/Button/Button";
import { GraphNode } from "./GraphNode";
import type { GraphLayout } from "./layout";
import styles from "./GraphCanvas.module.css";

const MIN_SCALE = 0.25;
const MAX_SCALE = 2.5;
const ZOOM_STEP = 0.15;

export interface GraphCanvasProps {
  layout: GraphLayout;
  tasksById: Record<string, Task>;
  onSelectTask?: (task: Task) => void;
  /** Exposed so Minimap can render the same viewport rect purely from state (§5) — no DOM measurement. */
  onViewChange?: (view: { translateX: number; translateY: number; scale: number }) => void;
}

/**
 * Pan/zoom state lives entirely here — translateX/Y/scale — never derived
 * from getBoundingClientRect (§5), so this and Minimap stay testable and
 * correct under happy-dom, which returns all-zero DOM geometry.
 */
export function GraphCanvas({ layout, tasksById, onSelectTask, onViewChange }: GraphCanvasProps) {
  const [translateX, setTranslateX] = useState(0);
  const [translateY, setTranslateY] = useState(0);
  const [scale, setScale] = useState(1);
  const dragState = useRef<{ startX: number; startY: number; originX: number; originY: number } | null>(
    null,
  );

  const emit = useCallback(
    (next: { translateX: number; translateY: number; scale: number }) => {
      onViewChange?.(next);
    },
    [onViewChange],
  );

  const setView = useCallback(
    (tx: number, ty: number, s: number) => {
      setTranslateX(tx);
      setTranslateY(ty);
      setScale(s);
      emit({ translateX: tx, translateY: ty, scale: s });
    },
    [emit],
  );

  const handleMouseMove = useCallback(
    (e: MouseEvent) => {
      if (!dragState.current) return;
      const dx = e.clientX - dragState.current.startX;
      const dy = e.clientY - dragState.current.startY;
      setView(dragState.current.originX + dx, dragState.current.originY + dy, scale);
    },
    [scale, setView],
  );

  const handleMouseUp = useCallback(() => {
    dragState.current = null;
    window.removeEventListener("mousemove", handleMouseMove);
    window.removeEventListener("mouseup", handleMouseUp);
  }, [handleMouseMove]);

  function handleMouseDown(e: React.MouseEvent) {
    dragState.current = {
      startX: e.clientX,
      startY: e.clientY,
      originX: translateX,
      originY: translateY,
    };
    window.addEventListener("mousemove", handleMouseMove);
    window.addEventListener("mouseup", handleMouseUp);
  }

  function clampScale(s: number): number {
    return Math.min(MAX_SCALE, Math.max(MIN_SCALE, s));
  }

  function handleWheel(e: ReactWheelEvent<SVGSVGElement>) {
    if (!e.ctrlKey) return;
    e.preventDefault();
    const next = clampScale(scale - e.deltaY * 0.01);
    setView(translateX, translateY, next);
  }

  function zoomBy(delta: number) {
    setView(translateX, translateY, clampScale(scale + delta));
  }

  function resetView() {
    setView(0, 0, 1);
  }

  return (
    <div className={styles.wrapper}>
      <svg
        className={styles.canvas}
        onMouseDown={handleMouseDown}
        onWheel={handleWheel}
        data-testid="graph-canvas-svg"
      >
        <g transform={`translate(${translateX}, ${translateY}) scale(${scale})`}>
          {layout.edges.map((edge) => {
            const from = layout.nodes.find((n) => n.id === edge.from);
            const to = layout.nodes.find((n) => n.id === edge.to);
            if (!from || !to) return null;
            return (
              <line
                key={edge.id}
                x1={from.x}
                y1={from.y}
                x2={to.x}
                y2={to.y}
                className={edge.inCycle ? styles.edgeCycle : styles.edge}
              />
            );
          })}
          {layout.nodes.map((node) => (
            <GraphNode key={node.id} node={node} tasksById={tasksById} onClick={onSelectTask} />
          ))}
        </g>
      </svg>
      <div className={styles.controls}>
        <Button variant="secondary" size="sm" onClick={() => zoomBy(ZOOM_STEP)}>
          +
        </Button>
        <Button variant="secondary" size="sm" onClick={() => zoomBy(-ZOOM_STEP)}>
          −
        </Button>
        <Button variant="secondary" size="sm" onClick={resetView}>
          Reset
        </Button>
      </div>
    </div>
  );
}
