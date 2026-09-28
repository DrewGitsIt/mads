package api_test

// Session-start's agent="auto" path (agent-concept redesign §4) is now a
// Fast Model one-shot over the persona registry (pkg/personapick), with
// fallback on the failure path (the workspace's default persona, or
// "none"). The old "capability-inference + on-demand generation" mechanism
// (pkg/autostaff + pkg/agentgen) is no longer called at session start —
// it is still used for the Library editor's explicit "generate a persona"
// action (services/router/internal/api/agents.go handleGenerateAgent).
//
// The concrete router tests for the new behavior live in sessions_test.go
// (TestCreateSessionAgentAuto* below); this file is a documentation anchor
// for the design decision that the persona is *picked* at session start,
// not *generated*.
