import { useEffect, useState } from "react";
import { Button } from "../../components/Button/Button";
import { Toast } from "../../components/Toast/Toast";
import type { Session, Task } from "../../router/types";
import { ChipStrip } from "./picker/ChipStrip";
import { SessionPicker } from "./picker/SessionPicker";
import { ObservationsInput } from "./ObservationsInput";
import { GenerationProgress } from "./generation/GenerationProgress";
import { DPACard } from "./dpa/DPACard";
import { acceptedDpaCount } from "./dpa/dpaEligibility";
import { FollowUpCta } from "./FollowUpCta";
import { RetroHistoryList } from "./history/RetroHistoryList";
import { useRetroGeneration, type GenerationPhase } from "./state/useRetroGeneration";
import { useRetroDetail } from "./state/useRetroDetail";
import { useRetroHistory } from "./state/useRetroHistory";
import { useSessionsForPicker } from "./state/useSessionsForPicker";
import styles from "./RetroSurface.module.css";

type SurfacePhase = "picking" | "generating" | "reviewing";

function computeSurfacePhase(phase: GenerationPhase, viewedRetroId: string | null): SurfacePhase {
  if (viewedRetroId !== null) return "reviewing";
  if (phase === "starting" || phase === "gathering" || phase === "generating" || phase === "completed") {
    return "generating";
  }
  return "picking";
}

/** Top-level Retro surface (registered in App.tsx) — owns the picking/generating/reviewing phase. */
export function RetroSurface() {
  const [selectedSessionIds, setSelectedSessionIds] = useState<string[]>([]);
  const [observations, setObservations] = useState("");
  const [viewedRetroId, setViewedRetroId] = useState<string | null>(null);
  const [followUpResult, setFollowUpResult] = useState<{ sessionId: string; taskCount: number } | null>(
    null,
  );

  const generation = useRetroGeneration();
  const sessionsForPicker = useSessionsForPicker();
  const history = useRetroHistory();
  const detail = useRetroDetail(viewedRetroId);

  useEffect(() => {
    if (generation.phase === "completed" && generation.retroId && viewedRetroId !== generation.retroId) {
      setViewedRetroId(generation.retroId);
    }
  }, [generation.phase, generation.retroId, viewedRetroId]);

  useEffect(() => {
    if (generation.phase === "cancelled" || generation.phase === "failed") {
      generation.reset();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- generation is a fresh object each render; only phase/reset matter here
  }, [generation.phase, generation.reset]);

  useEffect(() => {
    setFollowUpResult(null);
  }, [viewedRetroId]);

  const surfacePhase = computeSurfacePhase(generation.phase, viewedRetroId);

  const toggleSession = (id: string) => {
    setSelectedSessionIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  };

  const handleGenerate = () => {
    void generation.start(selectedSessionIds, observations);
  };

  const handleFollowUpCreated = (result: { session: Session; tasks: Task[] }) => {
    setFollowUpResult({ sessionId: result.session.id, taskCount: result.tasks.length });
  };

  const handleStartNew = () => {
    setViewedRetroId(null);
    generation.reset();
    setSelectedSessionIds([]);
    setObservations("");
  };

  return (
    <div className={styles.surface}>
      {surfacePhase === "picking" && (
        <div className={styles.section}>
          <SessionPicker
            sessions={sessionsForPicker.sessions}
            selectedIds={selectedSessionIds}
            onToggle={toggleSession}
            onClear={() => setSelectedSessionIds([])}
          />
          <ObservationsInput value={observations} onChange={setObservations} />
          <Button variant="primary" onClick={handleGenerate} disabled={selectedSessionIds.length === 0}>
            Generate retro
          </Button>
          {generation.error && (
            <Toast tone="bad" title="Couldn't generate retro" description={generation.error} />
          )}
          <RetroHistoryList retros={history.retros} onSelect={setViewedRetroId} />
        </div>
      )}

      {surfacePhase === "generating" && (
        <div className={styles.section}>
          <ChipStrip sessionIds={selectedSessionIds} sessions={sessionsForPicker.sessions} />
          <GenerationProgress phase={generation.phase} onCancel={generation.cancel} />
        </div>
      )}

      {surfacePhase === "reviewing" && detail.retro && (
        <div className={styles.section}>
          <ChipStrip sessionIds={detail.retro.session_ids} sessions={sessionsForPicker.sessions} />
          <div className={styles.dpaList}>
            {detail.retro.dpas.map((dpa) => (
              <DPACard key={dpa.id} dpa={dpa} onToggle={detail.toggleDpa} />
            ))}
          </div>
          <FollowUpCta
            retroId={detail.retro.id}
            acceptedCount={acceptedDpaCount(detail.retro.dpas)}
            followUpSessionId={detail.retro.follow_up_session_id ?? followUpResult?.sessionId}
            onCreated={handleFollowUpCreated}
          />
          {followUpResult && (
            <Toast
              tone="ok"
              title="Tasks imported"
              description={`${followUpResult.taskCount} tasks imported`}
            />
          )}
          <Button variant="ghost" onClick={handleStartNew}>
            Start new retro
          </Button>
        </div>
      )}

      {surfacePhase === "reviewing" && !detail.retro && (
        <div className={styles.section}>Loading retro…</div>
      )}
    </div>
  );
}
