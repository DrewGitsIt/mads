import { Card } from "../../components/Card/Card";
import { Chip } from "../../components/Chip/Chip";
import type { Agent } from "../../router/types";
import styles from "./AgentCard.module.css";

const QUADRANT_LABEL: Record<string, string> = {
  autonomous_executor: "Autonomous executor",
  technical_partner: "Technical partner",
  investigator: "Investigator",
  research_analyst: "Research analyst",
};

export interface AgentCardProps {
  agent: Agent;
  onClick: () => void;
}

export function AgentCard({ agent, onClick }: AgentCardProps) {
  return (
    <Card hoverable className={styles.card}>
      <button type="button" className={styles.trigger} onClick={onClick}>
        <div className={styles.name}>
          {agent.name}
          {agent.system && (
            <Chip tone="info" className={styles.systemChip}>
              System
            </Chip>
          )}
        </div>
        {agent.description && <div className={styles.description}>{agent.description}</div>}
        {agent.quadrant && (
          <Chip tone="neutral">{QUADRANT_LABEL[agent.quadrant] ?? agent.quadrant}</Chip>
        )}
      </button>
    </Card>
  );
}
