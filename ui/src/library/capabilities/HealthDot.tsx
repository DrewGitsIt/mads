import { useEffect, useRef, useState } from "react";
import { routerGet } from "../../router/client";
import { subscribeToEvents } from "../../router/events";
import type { Capability, CapabilityHealth } from "../../router/types";
import styles from "./HealthDot.module.css";

export interface HealthDotProps {
  capabilityId: string;
  health?: CapabilityHealth;
}

/** Live health indicator — refetches its own capability on capability.health_changed. */
export function HealthDot({ capabilityId, health: initialHealth }: HealthDotProps) {
  const [health, setHealth] = useState(initialHealth);
  const mounted = useRef(true);

  useEffect(() => {
    setHealth(initialHealth);
  }, [initialHealth]);

  useEffect(() => {
    mounted.current = true;
    const unsubscribe = subscribeToEvents({ types: ["capability.health_changed"] }, () => {
      routerGet<Capability>(`/capabilities/${capabilityId}`)
        .then((capability) => {
          if (mounted.current) setHealth(capability.health);
        })
        .catch(() => {
          // best-effort — the next event or a full shelf refetch will recover
        });
    });
    return () => {
      mounted.current = false;
      unsubscribe();
    };
  }, [capabilityId]);

  const tone = health === undefined ? "unknown" : health.healthy ? "healthy" : "unhealthy";
  const label =
    health === undefined ? "Health unknown" : health.healthy ? "Healthy" : (health.reason ?? "Unhealthy");

  return <span className={[styles.dot, styles[tone]].join(" ")} role="img" aria-label={label} title={label} />;
}
