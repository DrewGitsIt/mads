const SCAFFOLD = `Role: <what this agent is responsible for>
Approach: <how it should work — style, tools it favors, order of operations>
Boundaries: <what it must never do>
Output: <what a finished turn should look like>`;

/** Static structured-prompt guidance shown alongside the free-text prompt field. */
export function PromptScaffold() {
  return (
    <details>
      <summary>Prompt scaffold</summary>
      <pre>{SCAFFOLD}</pre>
    </details>
  );
}
