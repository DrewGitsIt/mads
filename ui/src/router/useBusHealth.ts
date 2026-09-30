import { useEffect, useState } from "react";
import { type BusHealth, getBusHealth, subscribeToBusHealth } from "./events";

/** The window's live-event connection health (see BusHealth), kept current. */
export function useBusHealth(): BusHealth {
  const [health, setHealth] = useState(getBusHealth);
  useEffect(() => {
    setHealth(getBusHealth());
    return subscribeToBusHealth(setHealth);
  }, []);
  return health;
}
