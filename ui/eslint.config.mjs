import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

export default tseslint.config(
  // e2e/ is the separate Playwright journey layer (ui/README.md's "Test
  // layers" section) — its fixtures.ts intentionally names a parameter `use`
  // (Playwright's own fixture API), which react-hooks' rules-of-hooks
  // otherwise misreads as the React `use()` hook.
  { ignores: ["dist", "dist-electron", "node_modules", "e2e"] },
  ...tseslint.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    plugins: { "react-hooks": reactHooks },
    rules: {
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",
      // Underscore-prefixed params are the established convention for
      // interface-mandated but intentionally-unused args (e.g. stub
      // methods matching a shared client interface).
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
      "no-restricted-syntax": [
        "error",
        {
          selector: "NewExpression[callee.name='EventSource']",
          message:
            "Don't open a raw EventSource — control-plane subscribers use " +
            "subscribeToEvents() (src/router/events.ts); data-plane streams " +
            "are already bounded in router/client.ts's streamSession and " +
            "pipeline/client/pipelineClient.ts's streamTaskOutput.",
        },
      ],
    },
  },
  {
    files: [
      "src/router/events.ts",
      "src/router/client.ts",
      "src/pipeline/client/pipelineClient.ts",
    ],
    rules: { "no-restricted-syntax": "off" },
  },
);
