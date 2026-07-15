// Recorded stage.*/pipeline.*/task.*/note.created event sequence + final task
// snapshot from a real `engine.Run` against `providers/fake`
// (services/pipeline/engine/engine_test.go's TestDefaultTemplateReviewLoopBack),
// captured once by hand per ui/src/pipeline/HANDOFF.md §5. Regenerate by hand
// only if the default-implementation template or that test's scenario
// changes — this is a one-time checked-in recording, not a live generator.
//
// Scenario: 3 tasks (core lib ← flaky api ← cli) on the default template
// (architect → executor → build → code_review → merge → verify).
// "flaky api" fails code_review every time: 2 loop-backs to executor
// (max_loops: 2), then blocks. "cli" never starts (dependency never done).
import type { Envelope, Task } from "../../router/types";

export const recordedSessionId = "ses_eb2fb7bc91e17d85";

export const recordedTaskIds = {
  coreLib: "tsk_6a6c5699d1c6a46a",
  flakyApi: "tsk_5c24de64083d0216",
  cli: "tsk_6c6461ccc082719d",
} as const;

export const recordedEnvelopes: Envelope[] = [
  {
    "seq": 2,
    "timestamp": "2026-07-15T05:30:12.262609722Z",
    "type": "task.created",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "draft": false,
      "title": "core lib"
    }
  },
  {
    "seq": 3,
    "timestamp": "2026-07-15T05:30:12.262651641Z",
    "type": "task.created",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "draft": false,
      "title": "flaky api"
    }
  },
  {
    "seq": 4,
    "timestamp": "2026-07-15T05:30:12.262677469Z",
    "type": "task.created",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6c6461ccc082719d",
    "actor": "pipeline",
    "payload": {
      "draft": false,
      "title": "cli"
    }
  },
  {
    "seq": 5,
    "timestamp": "2026-07-15T05:30:12.262963687Z",
    "type": "task.updated",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline"
  },
  {
    "seq": 6,
    "timestamp": "2026-07-15T05:30:12.263189522Z",
    "type": "task.updated",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6c6461ccc082719d",
    "actor": "pipeline"
  },
  {
    "seq": 7,
    "timestamp": "2026-07-15T05:30:12.265401682Z",
    "type": "pipeline.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "actor": "pipeline",
    "payload": {
      "concurrency": 2,
      "template": "default-implementation"
    }
  },
  {
    "seq": 8,
    "timestamp": "2026-07-15T05:30:12.266350546Z",
    "type": "task.updated",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline"
  },
  {
    "seq": 9,
    "timestamp": "2026-07-15T05:30:12.266370173Z",
    "type": "task.status_changed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "from": "ready",
      "to": "in_progress"
    }
  },
  {
    "seq": 10,
    "timestamp": "2026-07-15T05:30:12.266519534Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "architect"
    }
  },
  {
    "seq": 11,
    "timestamp": "2026-07-15T05:30:12.266647625Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "architect"
    }
  },
  {
    "seq": 12,
    "timestamp": "2026-07-15T05:30:12.26686845Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "executor"
    }
  },
  {
    "seq": 13,
    "timestamp": "2026-07-15T05:30:12.266973648Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "executor"
    }
  },
  {
    "seq": 14,
    "timestamp": "2026-07-15T05:30:12.267050722Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "build"
    }
  },
  {
    "seq": 15,
    "timestamp": "2026-07-15T05:30:12.269117949Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "build"
    }
  },
  {
    "seq": 16,
    "timestamp": "2026-07-15T05:30:12.26932019Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "code_review"
    }
  },
  {
    "seq": 17,
    "timestamp": "2026-07-15T05:30:12.2694162Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "code_review"
    }
  },
  {
    "seq": 18,
    "timestamp": "2026-07-15T05:30:12.269533661Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "merge"
    }
  },
  {
    "seq": 19,
    "timestamp": "2026-07-15T05:30:12.270598684Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "merge"
    }
  },
  {
    "seq": 20,
    "timestamp": "2026-07-15T05:30:12.270706857Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "verify"
    }
  },
  {
    "seq": 21,
    "timestamp": "2026-07-15T05:30:12.271731514Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "stage": "verify"
    }
  },
  {
    "seq": 22,
    "timestamp": "2026-07-15T05:30:12.271883089Z",
    "type": "task.updated",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline"
  },
  {
    "seq": 23,
    "timestamp": "2026-07-15T05:30:12.271895903Z",
    "type": "task.status_changed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_6a6c5699d1c6a46a",
    "actor": "pipeline",
    "payload": {
      "from": "in_progress",
      "to": "done"
    }
  },
  {
    "seq": 24,
    "timestamp": "2026-07-15T05:30:12.272565612Z",
    "type": "task.updated",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline"
  },
  {
    "seq": 25,
    "timestamp": "2026-07-15T05:30:12.27258562Z",
    "type": "task.status_changed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "from": "ready",
      "to": "in_progress"
    }
  },
  {
    "seq": 26,
    "timestamp": "2026-07-15T05:30:12.272746723Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "architect"
    }
  },
  {
    "seq": 27,
    "timestamp": "2026-07-15T05:30:12.272860647Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "architect"
    }
  },
  {
    "seq": 28,
    "timestamp": "2026-07-15T05:30:12.272953402Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "executor"
    }
  },
  {
    "seq": 29,
    "timestamp": "2026-07-15T05:30:12.273100699Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "executor"
    }
  },
  {
    "seq": 30,
    "timestamp": "2026-07-15T05:30:12.273188143Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "build"
    }
  },
  {
    "seq": 31,
    "timestamp": "2026-07-15T05:30:12.274979621Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "build"
    }
  },
  {
    "seq": 32,
    "timestamp": "2026-07-15T05:30:12.27511726Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "code_review"
    }
  },
  {
    "seq": 33,
    "timestamp": "2026-07-15T05:30:12.275234461Z",
    "type": "stage.failed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "reason": "agent code-reviewer failed the stage: STAGE: fail — missing error handling",
      "stage": "code_review"
    }
  },
  {
    "seq": 34,
    "timestamp": "2026-07-15T05:30:12.275421041Z",
    "type": "note.created",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline"
  },
  {
    "seq": 35,
    "timestamp": "2026-07-15T05:30:12.275518435Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "executor"
    }
  },
  {
    "seq": 36,
    "timestamp": "2026-07-15T05:30:12.275603454Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "executor"
    }
  },
  {
    "seq": 37,
    "timestamp": "2026-07-15T05:30:12.275708071Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "build"
    }
  },
  {
    "seq": 38,
    "timestamp": "2026-07-15T05:30:12.277310534Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "build"
    }
  },
  {
    "seq": 39,
    "timestamp": "2026-07-15T05:30:12.27742494Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "code_review"
    }
  },
  {
    "seq": 40,
    "timestamp": "2026-07-15T05:30:12.277571876Z",
    "type": "stage.failed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "reason": "agent code-reviewer failed the stage: STAGE: fail — missing error handling",
      "stage": "code_review"
    }
  },
  {
    "seq": 41,
    "timestamp": "2026-07-15T05:30:12.277776821Z",
    "type": "note.created",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline"
  },
  {
    "seq": 42,
    "timestamp": "2026-07-15T05:30:12.27790363Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "executor"
    }
  },
  {
    "seq": 43,
    "timestamp": "2026-07-15T05:30:12.277986846Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "executor"
    }
  },
  {
    "seq": 44,
    "timestamp": "2026-07-15T05:30:12.278060945Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "build"
    }
  },
  {
    "seq": 45,
    "timestamp": "2026-07-15T05:30:12.27979687Z",
    "type": "stage.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "build"
    }
  },
  {
    "seq": 46,
    "timestamp": "2026-07-15T05:30:12.279952913Z",
    "type": "stage.started",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "stage": "code_review"
    }
  },
  {
    "seq": 47,
    "timestamp": "2026-07-15T05:30:12.280069853Z",
    "type": "stage.failed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "reason": "agent code-reviewer failed the stage: STAGE: fail — missing error handling",
      "stage": "code_review"
    }
  },
  {
    "seq": 48,
    "timestamp": "2026-07-15T05:30:12.280261974Z",
    "type": "note.created",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline"
  },
  {
    "seq": 49,
    "timestamp": "2026-07-15T05:30:12.280429619Z",
    "type": "task.updated",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline"
  },
  {
    "seq": 50,
    "timestamp": "2026-07-15T05:30:12.280441021Z",
    "type": "task.status_changed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "task_id": "tsk_5c24de64083d0216",
    "actor": "pipeline",
    "payload": {
      "from": "in_progress",
      "to": "blocked"
    }
  },
  {
    "seq": 51,
    "timestamp": "2026-07-15T05:30:12.28113183Z",
    "type": "pipeline.completed",
    "session_id": "ses_eb2fb7bc91e17d85",
    "actor": "pipeline",
    "payload": {
      "blocked": 1,
      "done": 1,
      "outcome": "incomplete",
      "skipped": 1
    }
  }
];

export const recordedTasks: Task[] = [
  {
    "id": "tsk_6a6c5699d1c6a46a",
    "session_id": "ses_eb2fb7bc91e17d85",
    "status": "done",
    "retry_count": 0,
    "created_at": "2026-07-15T05:30:12.262557313Z",
    "updated_at": "2026-07-15T05:30:12.2718306Z",
    "title": "core lib",
    "source": "user"
  },
  {
    "id": "tsk_5c24de64083d0216",
    "session_id": "ses_eb2fb7bc91e17d85",
    "status": "blocked",
    "dependencies": [
      "tsk_6a6c5699d1c6a46a"
    ],
    "retry_count": 0,
    "created_at": "2026-07-15T05:30:12.26263533Z",
    "updated_at": "2026-07-15T05:30:12.280379866Z",
    "title": "flaky api",
    "source": "user",
    "notes": [
      {
        "id": 1,
        "type": "observation",
        "content": "stage code_review failed (loop-back 1/2 to executor): This won't do.\nSTAGE: fail — missing error handling",
        "source": "pipeline",
        "status": "confirmed",
        "timestamp": "2026-07-15T05:30:12.275402166Z"
      },
      {
        "id": 2,
        "type": "observation",
        "content": "stage code_review failed (loop-back 2/2 to executor): This won't do.\nSTAGE: fail — missing error handling",
        "source": "pipeline",
        "status": "confirmed",
        "timestamp": "2026-07-15T05:30:12.277747115Z"
      },
      {
        "id": 3,
        "type": "blocker",
        "content": "stage code_review: This won't do.\nSTAGE: fail — missing error handling",
        "source": "pipeline",
        "status": "confirmed",
        "timestamp": "2026-07-15T05:30:12.280236997Z"
      }
    ]
  },
  {
    "id": "tsk_6c6461ccc082719d",
    "session_id": "ses_eb2fb7bc91e17d85",
    "status": "ready",
    "dependencies": [
      "tsk_5c24de64083d0216"
    ],
    "retry_count": 0,
    "created_at": "2026-07-15T05:30:12.262664545Z",
    "updated_at": "2026-07-15T05:30:12.263131152Z",
    "title": "cli",
    "source": "user"
  }
];
