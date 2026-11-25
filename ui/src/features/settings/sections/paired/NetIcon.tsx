import styles from "./Mesh.module.css";

/** A network's kind as an icon: a VPN (shield), a local network (Wi-Fi), the internet (globe). */
export function NetIcon({ icon }: { icon: "overlay" | "wifi" | "public" }) {
  return (
    <span className={styles.netIcon} aria-hidden="true" data-net-icon={icon}>
      {icon === "overlay" ? (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round">
          <path d="M8 1.8 13 3.8v3.7c0 3.1-2.1 5.4-5 6.7-2.9-1.3-5-3.6-5-6.7V3.8z" />
          <path d="m5.8 8 1.6 1.6L10.4 6.6" />
        </svg>
      ) : icon === "public" ? (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4">
          <circle cx="8" cy="8" r="6.2" />
          <path d="M1.8 8h12.4M8 1.8c1.8 1.9 2.6 4 2.6 6.2S9.8 12.3 8 14.2C6.2 12.3 5.4 10.2 5.4 8S6.2 3.7 8 1.8z" />
        </svg>
      ) : (
        <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round">
          <path d="M1.8 6a9 9 0 0 1 12.4 0" />
          <path d="M4 8.4a5.8 5.8 0 0 1 8 0" />
          <path d="M6.2 10.8a2.6 2.6 0 0 1 3.6 0" />
          <circle cx="8" cy="13" r=".7" fill="currentColor" stroke="none" />
        </svg>
      )}
    </span>
  );
}
