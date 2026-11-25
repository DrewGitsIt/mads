import { Fragment, type ReactNode } from "react";
import { VPN_GUIDES } from "../network";
import styles from "../Mesh.module.css";

/**
 * "Give your devices a network in common" (U1's None of these): what mads
 * does, then tips by situation, with the VPN install guides. Reused
 * wherever "no shared network" is the answer: Set up mesh, pairing by code
 * (F2), and Troubleshoot connection.
 */
export function NoSharedNetwork({ lead }: { lead?: ReactNode }) {
  return (
    <div className={styles.tips} data-tips="no_shared_network">
      {lead && <p className={styles.lede}>{lead}</p>}
      <p className={styles.lede}>
        mads connects devices over a network they <b>both</b> share. It doesn't make one.
      </p>
      <div className={styles.tipsGroup}>
        <h3 className={styles.tipsHead}>Devices in the same place</h3>
        <ul>
          <li>
            Join the <b>same Wi-Fi network name</b> on both. A wired device and a Wi-Fi device on the same router usually work too.
          </li>
          <li>
            Avoid the <b>guest network</b>: it usually stops devices from seeing each other.
          </li>
          <li>
            If your router shows separate <b>2.4 GHz and 5 GHz network names</b>, join the same one on both.
          </li>
          <li>
            Some routers have <b>client or AP isolation</b> turned on, which has the same effect. Check your router's settings.
          </li>
        </ul>
      </div>
      <div className={styles.tipsGroup}>
        <h3 className={styles.tipsHead}>Devices in different places</h3>
        <ul>
          <li>
            Install the same VPN on each device and sign in to the same account:{" "}
            {VPN_GUIDES.map(([name, url], i) => (
              <Fragment key={name}>
                {i > 0 && ", "}
                <a href={url} target="_blank" rel="noreferrer">
                  {name}
                </a>
              </Fragment>
            ))}
            . It puts your devices on one private network wherever they are.
          </li>
          <li>
            Then come back here and choose <b>Check again</b>.
          </li>
        </ul>
      </div>
      <p className={styles.later}>Later: a mads relay will connect devices that share no network.</p>
    </div>
  );
}
