import type { GraphLayout } from "./layout";
import styles from "./Minimap.module.css";

const MINIMAP_WIDTH = 160;
const MINIMAP_HEIGHT = 100;
const PADDING = 8;

export interface MinimapView {
  translateX: number;
  translateY: number;
  scale: number;
}

export interface MinimapProps {
  layout: GraphLayout;
  view: MinimapView;
  /** Logical size of the GraphCanvas viewport (not measured from the DOM — passed in, see §5). */
  viewportSize: { width: number; height: number };
  onRecenter?: (view: MinimapView) => void;
}

/**
 * Scaled-down mirror of the graph plus a viewport rect. All geometry here is
 * derived from layout.extent + the view/viewportSize props, never from
 * ref.current.getBoundingClientRect() (§5) — that returns all-zero under
 * happy-dom and would make this component untestable.
 */
export function Minimap({ layout, view, viewportSize, onRecenter }: MinimapProps) {
  const { minX, minY, maxX, maxY } = layout.extent;
  const extentWidth = Math.max(1, maxX - minX);
  const extentHeight = Math.max(1, maxY - minY);
  const scale = Math.min(
    (MINIMAP_WIDTH - PADDING * 2) / extentWidth,
    (MINIMAP_HEIGHT - PADDING * 2) / extentHeight,
  );

  function toMinimap(x: number, y: number) {
    return { x: PADDING + (x - minX) * scale, y: PADDING + (y - minY) * scale };
  }

  // The visible logical region is the inverse of GraphCanvas's transform:
  // translate(tx,ty) scale(s) applied to content means content point p maps
  // to screen (p*s + t); the visible logical rect is [(0-t)/s, (viewport-t)/s].
  const visibleMinX = -view.translateX / view.scale;
  const visibleMinY = -view.translateY / view.scale;
  const visibleWidth = viewportSize.width / view.scale;
  const visibleHeight = viewportSize.height / view.scale;

  const rectTopLeft = toMinimap(visibleMinX, visibleMinY);
  const rectWidth = visibleWidth * scale;
  const rectHeight = visibleHeight * scale;

  // clickX/clickY are the click position within the minimap SVG, taken
  // directly from the MouseEvent (like ResizablePanel's clientX/clientY
  // deltas) rather than getBoundingClientRect, which is all-zero under
  // happy-dom (§5).
  function handleClick(e: React.MouseEvent<SVGSVGElement>) {
    if (!onRecenter) return;
    const clickX = e.clientX;
    const clickY = e.clientY;
    const logicalX = minX + (clickX - PADDING) / scale;
    const logicalY = minY + (clickY - PADDING) / scale;
    onRecenter({
      translateX: viewportSize.width / 2 - logicalX * view.scale,
      translateY: viewportSize.height / 2 - logicalY * view.scale,
      scale: view.scale,
    });
  }

  return (
    <svg
      className={styles.minimap}
      width={MINIMAP_WIDTH}
      height={MINIMAP_HEIGHT}
      onClick={handleClick}
      data-testid="minimap-svg"
    >
      {layout.nodes.map((node) => {
        const p = toMinimap(node.x, node.y);
        return <circle key={node.id} cx={p.x} cy={p.y} r={2} className={styles.node} />;
      })}
      <rect
        x={rectTopLeft.x}
        y={rectTopLeft.y}
        width={rectWidth}
        height={rectHeight}
        className={styles.viewportRect}
      />
    </svg>
  );
}
