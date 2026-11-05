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
];
