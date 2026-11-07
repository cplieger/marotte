// The shared ruleset is ./eslint.config.base.mjs, synced from cplieger/ci;
// this file adds marotte's deltas.
import tseslint from "typescript-eslint";

import baseConfig from "./eslint.config.base.mjs";

// The client traces its connection and SSE frames with console.debug. Set on the
// base's own rules entry so the base's test entry, which turns no-console off,
// still follows it.
const allowDebug = (config) =>
  config.files === undefined && config.rules?.["no-console"]
    ? {
        ...config,
        rules: { ...config.rules, "no-console": ["warn", { allow: ["warn", "error", "debug"] }] },
      }
    : config;

export default [
  ...baseConfig.map(allowDebug),
  { ignores: ["**/__test-helpers__/**"] },
  {
    // Typed by `npm run typecheck:tests` (tsconfig.test.json), not here.
    files: ["scripts/*.ts", "fc-strict-setup.ts"],
    ...tseslint.configs.disableTypeChecked,
  },
  {
    // A build script reports its measurement on stdout.
    files: ["scripts/*.ts"],
    rules: { "no-console": "off" },
  },
  {
    // dom.ts owns page-wide lookups. The audit walkers are serialised into the page and may use
    // only page globals (audit/walkers.ts), so they cannot import it.
    files: ["**/*.ts"],
    ignores: ["dom.ts", "audit/**", "**/*.test.ts", "scripts/**"],
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector:
            "MemberExpression[object.name='document'][property.name=/^(getElementById|querySelector|querySelectorAll)$/]",
          message: "Look elements up through dom.ts (byId, maybeEl, queryEl, queryAll).",
        },
        {
          selector:
            "MemberExpression[object.object.name='document'][object.property.name=/^(body|documentElement)$/][property.name=/^(querySelector|querySelectorAll|getElementsByClassName|getElementsByTagName)$/]",
          message:
            "Look elements up through dom.ts (queryEl, queryAll), which searches the parked surfaces too.",
        },
        {
          selector:
            "CallExpression > Identifier.arguments[name='document'], LogicalExpression > Identifier[name='document']",
          message:
            "Walk the page through dom.ts pageRoots(), which covers the parked surfaces too.",
        },
      ],
    },
  },
];
