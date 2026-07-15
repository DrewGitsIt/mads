// Local fixture standing in for a provider's agent/subagent export — no
// live provider-import backend exists anywhere yet (HANDOFF §5). Field
// names on the left are what a provider might expose; the mapping preview
// shows how they'd land on an Agent, preview only.
export interface ProviderAgentFixture {
  providerName: string;
  fields: { providerField: string; agentField: string; value: string }[];
}

export const IMPORT_FIXTURES: ProviderAgentFixture[] = [
  {
    providerName: "code-reviewer",
    fields: [
      { providerField: "name", agentField: "name", value: "code-reviewer" },
      {
        providerField: "description",
        agentField: "description",
        value: "Reviews diffs for correctness, security, and style issues.",
      },
      {
        providerField: "systemPrompt",
        agentField: "prompt",
        value: "You are a meticulous code reviewer...",
      },
      { providerField: "tools", agentField: "capabilities.tools", value: "read, grep, glob" },
    ],
  },
  {
    providerName: "test-writer",
    fields: [
      { providerField: "name", agentField: "name", value: "test-writer" },
      {
        providerField: "description",
        agentField: "description",
        value: "Writes unit tests for newly added functions.",
      },
      {
        providerField: "systemPrompt",
        agentField: "prompt",
        value: "You are a test-writing specialist...",
      },
      { providerField: "tools", agentField: "capabilities.tools", value: "read, write, shell" },
    ],
  },
];
